# Golimit

一个基于 Redis + Lua 的高性能分布式限流组件，当前同时支持 `Gin` 和 `GoFrame`。

## 特性

- 基于 Redis Lua 脚本，保证限流判断的原子性。
- 使用令牌桶算法，支持突发流量。
- 核心限流逻辑与 Web 框架解耦，便于扩展不同适配层。
- 限流组件异常时默认 fail-open，不阻塞业务请求。

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

## 自定义限流维度

如果你希望按用户、IP、租户等维度限流，可以在适配层外拼接不同的业务 key，例如：

```go
limitKey := "login_api:user:1001"
```

当前内置中间件默认直接使用你传入的 `key`。
