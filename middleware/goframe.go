package middleware

import (
	"net/http"

	"github.com/2474039695/golimit/limiter"
	"github.com/gogf/gf/v2/net/ghttp"
)

// NewGoFrame returns a GoFrame middleware handler.
func NewGoFrame(key string, l limiter.Limiter) func(r *ghttp.Request) {
	return func(r *ghttp.Request) {
		allowed, err := l.Allow(r.Context(), key, 1)
		if err != nil {
			// 组件设计原则：故障降级（Redis 挂了不应影响主业务）
			r.Middleware.Next()
			return
		}

		if !allowed {
			r.Response.WriteStatus(http.StatusTooManyRequests)
			r.Response.WriteJsonExit(map[string]any{
				"code":    http.StatusTooManyRequests,
				"message": "请求过于频繁，请稍后再试",
			})
			return
		}

		r.Middleware.Next()
	}
}
