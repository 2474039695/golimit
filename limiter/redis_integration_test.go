package limiter

import (
	"context"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"os"
	"testing"
	"time"
)

// 集成测试仅访问自己创建的唯一前缀，不清空调用方 Redis 数据库。
func TestRealRedisIntegration(t *testing.T) {
	addr := os.Getenv("GOLIMIT_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set GOLIMIT_TEST_REDIS_ADDR to run against a real Redis instance")
	}
	ctx := context.Background()
	c := redis.NewClient(&redis.Options{Addr: addr, ContextTimeoutEnabled: true, MaxRetries: -1})
	defer c.Close()
	prefix := fmt.Sprintf("golimit-test-%d", time.Now().UnixNano())
	cfg := Config{Rate: 1, Capacity: 2, KeyPrefix: prefix, Timeout: time.Second, IdleTTL: time.Second}
	l, err := NewRedis(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Del(ctx, prefix+":test", prefix+":corrupt")
	first, err := l.Check(ctx, "test", 2)
	if err != nil || !first.Allowed || first.Remaining != 0 {
		t.Fatalf("deduction: %+v %v", first, err)
	}
	denied, err := l.Check(ctx, "test", 1)
	if err != nil || denied.Allowed || denied.RetryAfter <= 0 || denied.RetryAfter > time.Second {
		t.Fatalf("denial: %+v %v", denied, err)
	}
	ttl, err := c.TTL(ctx, prefix+":test").Result()
	if err != nil || ttl < 2*time.Second {
		t.Fatalf("TTL: %s %v", ttl, err)
	}
	cfg.Rate = 2
	other, err := NewRedis(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Check(ctx, "test", 1); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("config collision: %v", err)
	}
	if err := c.HSet(ctx, prefix+":corrupt", "tokens", "bad", "last_time", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Check(ctx, "corrupt", 1); !errors.Is(err, ErrBackend) {
		t.Fatalf("corrupt state: %v", err)
	}
	if got := c.HGet(ctx, prefix+":corrupt", "tokens").Val(); got != "bad" {
		t.Fatalf("corrupt state was reset: %s", got)
	}
}
