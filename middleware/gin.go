package middleware

import (
	"context"
	"github.com/2474039695/golimit/limiter"
	"github.com/gin-gonic/gin"
)

// NewGinWithOptions 为 Gin 提供动态身份、成本、跳过规则和故障策略。
func NewGinWithOptions(l limiter.Limiter, opts Options) (gin.HandlerFunc, error) {
	opts, err := validateOptions(l, opts)
	if err != nil {
		return nil, err
	}
	return ginHandler(l, opts, ""), nil
}

// NewGin 保留固定键和故障放行的旧行为，新业务应显式使用 WithOptions 选择策略。
func NewGin(key string, l limiter.Limiter) gin.HandlerFunc {
	if err := limiter.ValidateKey(key); err != nil {
		panic(err)
	}
	opts, err := validateOptions(l, Options{PolicyName: "legacy", ErrorPolicy: FailOpen})
	if err != nil {
		panic(err)
	}
	return ginHandler(l, opts, key)
}
func ginHandler(l limiter.Limiter, opts Options, key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if opts.SkipFunc != nil && opts.SkipFunc(c.Request) {
			c.Next()
			return
		}
		// 只把限流阶段纳入预算，不给后续业务处理施加这个短 deadline。
		ctx, cancel := context.WithTimeout(c.Request.Context(), opts.Timeout)
		d := decide(ctx, c.Request.WithContext(ctx), l, opts, key)
		cancel()
		writeHeaders(c.Writer.Header(), d)
		if d.status != 0 {
			c.AbortWithStatusJSON(d.status, gin.H{"code": d.status, "message": responseMessage(d.status)})
			return
		}
		c.Next()
	}
}

// New 是旧 Gin 入口。
//
// Deprecated: 使用 NewGin 或 NewGinWithOptions。
func New(key string, l limiter.Limiter) gin.HandlerFunc { return NewGin(key, l) }
