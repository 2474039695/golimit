# Golimit

一个基于 Redis + Lua 的高性能分布式限流中间件，专为 Go (Gin) 框架设计。

## 特性

- 🚀 **高性能**：基于 Redis Lua 脚本，保证原子性。
- 🛡️ **令牌桶算法**：支持突发流量。
- 🔌 **易于集成**：一行代码接入 Gin。

## 安装

```bash
go get github.com/2474039695/golimit