# Golimit

一个基于 Redis + Lua 的高性能分布式限流组件，当前同时支持 `Gin`、`GoFrame` 和 Prometheus 指标暴露。

## 特性

- 基于 Redis Lua 脚本，保证限流判断的原子性。
- 使用令牌桶算法，支持突发流量。
- 核心限流逻辑与 Web 框架解耦，便于扩展不同适配层。
- 限流组件异常时默认 fail-open，不阻塞业务请求。
- 支持 Prometheus 指标，可监控放行数、拒绝数、错误数、令牌消耗和限流耗时。

## 安装

```bash
go get github.com/2474039695/golimit
```

## Gin 接入

```go
package main

import (
	"github.com/2474039695/golimit/limiter"
	"github.com/2474039695/golimit/middleware"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	l := limiter.New(rdb, limiter.Config{
		Rate:     10,
		Capacity: 20,
	})

	r := gin.Default()
	r.Use(middleware.New("login_api", l))

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "pong"})
	})

	_ = r.Run(":8080")
}
```

## GoFrame 接入

```go
package main

import (
	"github.com/2474039695/golimit/limiter"
	"github.com/2474039695/golimit/middleware"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/redis/go-redis/v9"
)

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	l := limiter.New(rdb, limiter.Config{
		Rate:     10,
		Capacity: 20,
	})

	s := g.Server()
	s.Use(middleware.NewGoFrame("login_api", l))
	s.BindHandler("/ping", func(r *ghttp.Request) {
		r.Response.WriteJson(map[string]string{"message": "pong"})
	})

	s.Run()
}
```

## Prometheus 指标接入

### Gin 示例

```go
package main

import (
	"github.com/2474039695/golimit/limiter"
	"github.com/2474039695/golimit/metrics"
	"github.com/2474039695/golimit/middleware"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	cfg := limiter.Config{
		Rate:     10,
		Capacity: 20,
	}

	baseLimiter := limiter.New(rdb, cfg)
	metricLimiter := metrics.WrapPrometheus(baseLimiter, cfg, metrics.Options{})

	r := gin.Default()
	r.GET("/metrics", gin.WrapH(metrics.Handler()))
	r.Use(middleware.New("login_api", metricLimiter))

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "pong"})
	})

	_ = r.Run(":8080")
}
```

### GoFrame 示例

```go
package main

import (
	"github.com/2474039695/golimit/limiter"
	"github.com/2474039695/golimit/metrics"
	"github.com/2474039695/golimit/middleware"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/redis/go-redis/v9"
)

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	cfg := limiter.Config{
		Rate:     10,
		Capacity: 20,
	}

	baseLimiter := limiter.New(rdb, cfg)
	metricLimiter := metrics.WrapPrometheus(baseLimiter, cfg, metrics.Options{})

	s := g.Server()
	s.Use(middleware.NewGoFrame("login_api", metricLimiter))

	metricsHandler := metrics.Handler()
	s.BindHandler("/metrics", func(r *ghttp.Request) {
		metricsHandler.ServeHTTP(r.Response.Writer, r.Request)
	})

	s.BindHandler("/ping", func(r *ghttp.Request) {
		r.Response.WriteJson(map[string]string{"message": "pong"})
	})

	s.Run()
}
```

### 默认暴露的指标

- `golimit_limiter_requests_total{key,result}`：限流请求总数，`result` 为 `allow`、`deny`、`error`。
- `golimit_limiter_request_tokens_total{key,result}`：请求消耗的令牌总数。
- `golimit_limiter_allow_duration_seconds{key}`：一次限流判断耗时。
- `golimit_limiter_config_rate{key}`：当前限流速率配置。
- `golimit_limiter_config_capacity{key}`：当前桶容量配置。

### 常用 PromQL

```promql
sum(rate(golimit_limiter_requests_total{result="allow"}[1m])) by (key)
sum(rate(golimit_limiter_requests_total{result="deny"}[1m])) by (key)
sum(golimit_limiter_requests_total{result="deny"}) by (key)
histogram_quantile(0.95, sum(rate(golimit_limiter_allow_duration_seconds_bucket[5m])) by (le, key))
```

## 自定义限流维度

如果你希望按用户、IP、租户等维度限流，可以在适配层外拼接不同的业务 key，例如：

```go
limitKey := "login_api:user:1001"
```

当前内置中间件默认直接使用你传入的 `key`。为了避免 Prometheus 指标出现高基数问题，不建议把动态的 `user_id`、`ip` 直接作为指标 label 使用。
