package middleware

import (
	"math"
	"net/http"
	"strconv"
)

// writeHeaders 只暴露已知元数据。Reset 表示补满的秒数，使用相对时间避免应用时钟误差。
// X-RateLimit-* 是本库约定的兼容头，具体语义在 README 中说明。
func writeHeaders(h http.Header, d decision) {
	if d.degraded {
		h.Set("X-RateLimit-Degraded", "true")
	}
	if !d.result.Metadata {
		return
	}
	h.Set("X-RateLimit-Limit", strconv.FormatInt(d.result.Capacity, 10))
	h.Set("X-RateLimit-Remaining", strconv.FormatInt(d.result.Remaining, 10))
	h.Set("X-RateLimit-Reset-After", strconv.FormatInt(int64(math.Ceil(d.result.ResetAfter.Seconds())), 10))
	if d.status == http.StatusTooManyRequests && d.result.RetryAfter > 0 {
		h.Set("Retry-After", strconv.FormatInt(int64(math.Ceil(d.result.RetryAfter.Seconds())), 10))
	}
}
func responseMessage(status int) string {
	switch status {
	case 429:
		return "请求过于频繁，请稍后再试"
	case 400:
		return "限流身份或请求成本无效"
	default:
		return "限流服务暂时不可用，请稍后再试"
	}
}
