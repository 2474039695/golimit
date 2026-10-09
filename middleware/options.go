package middleware

import (
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/2474039695/golimit/limiter"
)

// ErrorPolicy 只处理存储故障。无效 key/cost 和请求取消不能触发“出错就放行”。
type ErrorPolicy int

const (
	FailClosed    ErrorPolicy = iota // 新配置默认拒绝后端故障，返回 503。
	FailOpen                         // 后端故障继续业务，但不伪造成功扣减结果。
	LocalFallback                    // 后端故障使用独立、受容量限制的本地规则。
)

// Options 被两个框架共同使用。回调接收标准 Request，身份通常由前置认证写入 Context。
// 回调必须快速、无阻塞；如果进行 I/O，应主动遵守 Request.Context 的截止时间。
type Options struct {
	PolicyName  string                              // 固定规则名称，不能使用用户 ID/IP；同时作为 Redis key 的命名空间。
	KeyFunc     func(*http.Request) (string, error) // 返回身份部分，中间件自动拼接 PolicyName。
	CostFunc    func(*http.Request) (int64, error)  // 默认 1；批量请求可返回 N。
	SkipFunc    func(*http.Request) bool            // 默认不跳过，适合固定的健康检查等路径。
	ErrorPolicy ErrorPolicy
	Fallback    *limiter.LocalLimiter      // LocalFallback 模式必须设置，使用不同于 Redis 的进程内配额。
	Timeout     time.Duration              // 整个 Redis+兜底判断的总预算，默认 2 秒。
	OnError     func(*http.Request, error) // 日志/事件回调，收到原始后端错误；不可阻塞。
}

var policyPattern = regexp.MustCompile("^[a-zA-Z0-9_.-]{1,64}$")

func validateOptions(l limiter.Limiter, opts Options) (Options, error) {
	if limiter.IsNil(l) {
		return Options{}, fmt.Errorf("%w: nil limiter", limiter.ErrInvalidConfig)
	}
	if !policyPattern.MatchString(opts.PolicyName) {
		return Options{}, fmt.Errorf("%w: PolicyName must be a fixed identifier of 1..64 bytes", limiter.ErrInvalidConfig)
	}
	if opts.Timeout == 0 {
		opts.Timeout = 2 * time.Second
	}
	if opts.Timeout < 0 || opts.ErrorPolicy < FailClosed || opts.ErrorPolicy > LocalFallback {
		return Options{}, limiter.ErrInvalidConfig
	}
	if opts.ErrorPolicy == LocalFallback && opts.Fallback == nil {
		return Options{}, fmt.Errorf("%w: LocalFallback requires a local limiter", limiter.ErrInvalidConfig)
	}
	return opts, nil
}
