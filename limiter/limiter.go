package limiter

import (
	"context"
	"fmt"
	"reflect"
	"time"
)

// Limiter 保留基础接口，旧的自定义限流器可以继续使用。
type Limiter interface {
	Allow(context.Context, string, int64) (bool, error)
}

// DetailedLimiter 提供额度和等待时间，Check 不会预留未来额度。
type DetailedLimiter interface {
	Limiter
	Check(context.Context, string, int64) (Result, error)
}

// Result 是扣减后的状态。时间为估计值，后续竞争请求仍可能消耗令牌。
// Metadata=false 表示旧式 Limiter 没有详细信息，不得伪造额度响应头。
type Result struct {
	Allowed    bool
	Remaining  int64
	Capacity   int64
	RetryAfter time.Duration // 拒绝后再次尝试相同 count 所需的最短等待时间。
	ResetAfter time.Duration // 桶补满所需时间，不是窗口结束时间。
	Reason     string
	Metadata   bool
}

// Check 兼容仅返回 bool 的旧式 Limiter。
func Check(ctx context.Context, l Limiter, key string, count int64) (Result, error) {
	if IsNil(l) {
		return Result{}, fmt.Errorf("%w: nil limiter", ErrInvalidConfig)
	}
	if d, ok := l.(DetailedLimiter); ok {
		return d.Check(ctx, key, count)
	}
	allowed, err := l.Allow(ctx, key, count)
	return Result{Allowed: allowed}, err
}

// IsNil 同时识别 nil 接口和装进接口的 nil 指针，避免适配器创建后在请求期间 panic。
func IsNil(l Limiter) bool {
	if l == nil {
		return true
	}
	v := reflect.ValueOf(l)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return v.IsNil()
	}
	return false
}
