package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/2474039695/golimit/limiter"
)

// decision 保存一次限流判断的结果，供 Gin 和 GoFrame 统一处理。
type decision struct {
	result   limiter.Result
	status   int
	err      error
	degraded bool
}

// decide 只执行一次共享后端判断。超时后的扣减结果可能未知，绝不重复请求 Redis。
func decide(ctx context.Context, r *http.Request, l limiter.Limiter, opts Options, legacyKey string) decision {
	if err := ctx.Err(); err != nil {
		return decision{status: 503, err: err}
	}
	key := legacyKey
	if key == "" {
		key = opts.PolicyName
		if opts.KeyFunc != nil {
			suffix, err := opts.KeyFunc(r)
			if err != nil {
				return decision{status: 400, err: fmt.Errorf("%w: key extraction failed: %v", limiter.ErrInvalidRequest, err)}
			}
			if err := limiter.ValidateKey(suffix); err != nil {
				return decision{status: 400, err: err}
			}
			key += ":" + suffix
		}
	}
	cost := int64(1)
	if opts.CostFunc != nil {
		var err error
		cost, err = opts.CostFunc(r)
		if err != nil {
			return decision{status: 400, err: fmt.Errorf("%w: cost extraction failed: %v", limiter.ErrInvalidRequest, err)}
		}
	}
	if err := limiter.ValidateKey(key); err != nil {
		return decision{status: 400, err: err}
	}
	if cost < 1 {
		return decision{status: 400, err: limiter.ErrInvalidRequest}
	}
	res, err := limiter.Check(ctx, l, key, cost)
	if err == nil {
		return decisionFor(res)
	}
	if opts.OnError != nil {
		opts.OnError(r, err)
	}
	if errors.Is(err, limiter.ErrInvalidRequest) {
		return decision{status: 400, err: err}
	}
	if errors.Is(err, limiter.ErrInvalidConfig) {
		return decision{status: 500, err: err}
	}
	// 总预算耗尽或客户端已断开时，不再调用后备限流器或执行业务。
	if ctx.Err() != nil {
		return decision{status: 503, err: ctx.Err()}
	}
	switch opts.ErrorPolicy {
	case FailOpen:
		return decision{result: limiter.Result{Allowed: true, Reason: "fail_open"}, degraded: true}
	case LocalFallback:
		fallback, localErr := opts.Fallback.Check(ctx, key, cost)
		if localErr != nil {
			if opts.OnError != nil {
				opts.OnError(r, localErr)
			}
			return decision{status: 503, err: localErr, degraded: true}
		}
		d := decisionFor(fallback)
		d.degraded = true
		return d
	default:
		return decision{status: 503, err: err}
	}
}

func decisionFor(res limiter.Result) decision {
	if res.Allowed {
		return decision{result: res}
	}
	return decision{result: res, status: http.StatusTooManyRequests}
}
