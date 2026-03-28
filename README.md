# Golimit

一个基于 Redis + Lua 的高性能分布式限流中间件，专为 Go (Gin) 框架设计。

## 特性

- 🚀 **高性能**：基于 Redis Lua 脚本，保证原子性。
- 🛡️ **令牌桶算法**：支持突发流量。
- 🔌 **易于集成**：一行代码接入 Gin。

## 安装

```bash
go get github.com/2474039695/golimit
```
## 快速开始
```go
package main

import (
    "github.com/gin-gonic/gin"
    "github.com/go-redis/redis/v9"
    "github.com/2474039695/golimit/limiter"
    "github.com/2474039695/golimit/middleware"
)

func main() {
    // 1. 初始化 Redis
    rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})

    // 2. 初始化限流器 (每秒 10 个令牌，容量 20)
    l := limiter.New(rdb, limiter.Config{
        Rate:     10,
        Capacity: 20,
    })

    // 3. 使用中间件
    r := gin.Default()
    r.Use(middleware.New("login_api", l))

    r.GET("/ping", func(c *gin.Context) {
        c.JSON(200, "pong")
    })

    r.Run(":8080")
}
```