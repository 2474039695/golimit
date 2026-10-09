package limiter

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed token_bucket.lua
var luaScript string

// RedisLimiter 使用调用方提供的客户端，不修改其配置，也不负责关闭它。
type RedisLimiter struct {
	client redis.UniversalClient
	script *redis.Script
	cfg    Config
}

// NewRedis 启动时校验配置，调用方必须处理 error。
// 客户端必须启用 context 超时并关闭不确定结果的重试，否则会重复扣减。
// Cluster 的 MaxRedirects 循环也重试网络失败，所以严格模式要求同时关闭它。
func NewRedis(client redis.UniversalClient, cfg Config) (*RedisLimiter, error) {
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if err := validateClient(client); err != nil {
		return nil, err
	}
	return &RedisLimiter{client: client, script: redis.NewScript(luaScript), cfg: cfg}, nil
}
func validateClient(client redis.UniversalClient) error {
	bad := func(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidConfig, msg) }
	switch c := client.(type) {
	case *redis.Client:
		if c == nil {
			return bad("nil Redis client")
		}
		o := c.Options()
		// 创建 Client 后 MaxRetries=-1 已被 go-redis 标准化为 0。
		if !o.ContextTimeoutEnabled || o.MaxRetries != 0 {
			return bad("client requires ContextTimeoutEnabled=true and MaxRetries=-1 at creation")
		}
	case *redis.ClusterClient:
		if c == nil {
			return bad("nil Redis cluster client")
		}
		o := c.Options()
		if !o.ContextTimeoutEnabled || o.MaxRetries > 0 || o.MaxRedirects != 0 || o.ReadOnly {
			return bad("cluster requires ContextTimeoutEnabled=true, MaxRetries=-1, MaxRedirects=-1, ReadOnly=false")
		}
	default:
		return bad("only go-redis Client (including Sentinel failover) and ClusterClient are supported")
	}
	return nil
}

// New 保留旧签名；配置错误会 panic，应只用于启动时初始化。
//
// Deprecated: 使用 NewRedis 并处理 error。
func New(client redis.UniversalClient, cfg Config) *RedisLimiter {
	l, err := NewRedis(client, cfg)
	if err != nil {
		panic(err)
	}
	return l
}

// Configuration 返回配置副本，指标可以核对实际规则，避免显示错误配置。
func (rl *RedisLimiter) Configuration() Config { return rl.cfg }

// Allow 保留旧式判定接口，详细信息请调用 Check。
func (rl *RedisLimiter) Allow(ctx context.Context, key string, count int64) (bool, error) {
	res, err := rl.Check(ctx, key, count)
	return res.Allowed, err
}

// Check 使用较短的请求/组件截止时间，不重试网络失败。
// NOSCRIPT 明确说明脚本未执行，此时 go-redis 才安全回退到 EVAL。
func (rl *RedisLimiter) Check(ctx context.Context, key string, count int64) (Result, error) {
	if err := ValidateRequest(key, count, rl.cfg.Capacity); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, rl.cfg.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	values, err := rl.script.Run(ctx, rl.client, []string{rl.cfg.KeyPrefix + ":" + key}, rl.cfg.Rate, rl.cfg.Capacity, time.Now().UnixMilli(), count, ttlSeconds(rl.cfg), int64(rl.cfg.MaxTTL/time.Second)).Int64Slice()
	if err != nil {
		if redis.HasErrorPrefix(err, "LIMITER_CONFIG_MISMATCH") {
			return Result{}, fmt.Errorf("%w: same Redis key used with different Rate/Capacity", ErrInvalidConfig)
		}
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			err = context.DeadlineExceeded
		}
		return Result{}, &BackendError{Cause: err}
	}
	if len(values) != 4 || values[0] < 0 || values[0] > 1 || values[1] < 0 || values[1] > rl.cfg.Capacity || values[2] < 0 || values[3] < 0 ||
		values[2] > int64((1<<63-1)/time.Millisecond) || values[3] > int64((1<<63-1)/time.Millisecond) {
		return Result{}, &BackendError{Cause: fmt.Errorf("invalid script reply")}
	}
	reason := "allow"
	if values[0] == 0 {
		reason = "rate_limited"
	}
	return Result{Allowed: values[0] == 1, Remaining: values[1], Capacity: rl.cfg.Capacity, RetryAfter: time.Duration(values[2]) * time.Millisecond, ResetAfter: time.Duration(values[3]) * time.Millisecond, Reason: reason, Metadata: true}, nil
}
