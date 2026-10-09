package middleware

import (
	"context"
	"github.com/2474039695/golimit/limiter"
	"github.com/gogf/gf/v2/net/ghttp"
)

// NewGoFrameWithOptions 与 Gin 共用相同策略和 HTTP 响应语义。
func NewGoFrameWithOptions(l limiter.Limiter, opts Options) (func(*ghttp.Request), error) {
	opts, err := validateOptions(l, opts)
	if err != nil {
		return nil, err
	}
	return goFrameHandler(l, opts, ""), nil
}

// NewGoFrame 保留固定键、单令牌和故障放行的旧入口。
func NewGoFrame(key string, l limiter.Limiter) func(*ghttp.Request) {
	if err := limiter.ValidateKey(key); err != nil {
		panic(err)
	}
	opts, err := validateOptions(l, Options{PolicyName: "legacy", ErrorPolicy: FailOpen})
	if err != nil {
		panic(err)
	}
	return goFrameHandler(l, opts, key)
}
func goFrameHandler(l limiter.Limiter, opts Options, key string) func(*ghttp.Request) {
	return func(r *ghttp.Request) {
		if opts.SkipFunc != nil && opts.SkipFunc(r.Request) {
			r.Middleware.Next()
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), opts.Timeout)
		d := decide(ctx, r.Request.WithContext(ctx), l, opts, key)
		cancel()
		writeHeaders(r.Response.Header(), d)
		if d.status != 0 {
			r.Response.WriteStatus(d.status)
			r.Response.WriteJsonExit(map[string]any{"code": d.status, "message": responseMessage(d.status)})
			return
		}
		r.Middleware.Next()
	}
}
