package middleware

import (
	"net/http"

	"github.com/2474039695/golimit/limiter"
	"github.com/gin-gonic/gin"
)

// New returns a Gin middleware handler.
func New(key string, l limiter.Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed, err := l.Allow(c.Request.Context(), key, 1)
		if err != nil {
			// 组件设计原则：故障降级（Redis 挂了不应影响主业务）
			c.Next()
			return
		}

		if !allowed {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":    http.StatusTooManyRequests,
				"message": "请求过于频繁，请稍后再试",
			})
			return
		}

		c.Next()
	}
}
