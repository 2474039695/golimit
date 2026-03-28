package limiter

import (
	"context"
	"time"
)

// Limiter 定义了限流器的通用行为
type Limiter interface {
	Allow(ctx context.Context, key string, count int64) (bool, error)
}

// Config 用于配置限流器参数
type Config struct {
	Rate      float64       // 令牌生成速率 (每秒)
	Capacity  int64         // 桶容量
	KeyPrefix string        // Redis Key 前缀
	Timeout   time.Duration // 超时时间
}
