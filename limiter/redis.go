package limiter

import (
	"context"
	_ "embed"
	"fmt"
	"github.com/redis/go-redis/v9"
	"time"
)

//go:embed script.lua
var luaScript string

// RedisLimiter 是基于 Redis 的 Limiter 实现
type RedisLimiter struct {
	client redis.UniversalClient
	script *redis.Script
	cfg    Config
}

// New 创建一个新的分布式限流器
func New(client redis.UniversalClient, cfg Config) *RedisLimiter {
	// 设置默认值
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = "golimit"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = time.Second
	}

	return &RedisLimiter{
		client: client,
		script: redis.NewScript(luaScript),
		cfg:    cfg,
	}
}

// Allow 检查是否允许请求通过
func (rl *RedisLimiter) Allow(ctx context.Context, key string, count int64) (bool, error) {
	redisKey := fmt.Sprintf("%s:%s", rl.cfg.KeyPrefix, key)
	now := time.Now().UnixMilli()

	// 执行 Lua 脚本
	res, err := rl.script.Run(ctx, rl.client, []string{redisKey},
		rl.cfg.Rate, rl.cfg.Capacity, now, count).Int()

	if err != nil {
		return false, err
	}

	return res == 1, nil
}
