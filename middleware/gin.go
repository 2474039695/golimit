package middleware

import (
	"net/http"

	"github.com/2474039695/golimit/limiter" // 注意替换为你的模块路径
	"github.com/gin-gonic/gin"
)

// New 返回一个 Gin 限流中间件 Handler
func New(key string, l *limiter.RedisLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 这里可以扩展：根据 IP 或 UserID 拼接 key
		// 例如：limitKey := fmt.Sprintf("%s:%s", key, c.ClientIP())

		allow, err := l.Allow(c.Request.Context(), key, 1)

		if err != nil {
			// 组件设计原则：故障降级（Redis 挂了不应影响主业务）
			c.Next()
			return
		}

		if !allow {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":    429,
				"message": "请求过于频繁，请稍后再试",
			})
			return
		}

		c.Next()
	}
}
