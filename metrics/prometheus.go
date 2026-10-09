package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/2474039695/golimit/limiter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Options 的标签全部在创建包装器时确定，不允许从每次请求的动态 key 生成标签。
type Options struct {
	Namespace   string
	Subsystem   string
	Registerer  prometheus.Registerer
	PolicyName  string // 固定规则名，默认 default；不得使用用户 ID、IP、原始 URL。
	Backend     string // redis/local/custom；空值根据后端类型自动确定。
	MaxPolicies int    // 同一 registry/namespace/subsystem 的规则上限，默认 100。
}
type prometheusLimiter struct {
	next            limiter.Limiter
	cfg             limiter.Config
	policy, backend string
	collectors      *collectors
}
type collectors struct {
	checks      *prometheus.CounterVec
	requested   *prometheus.CounterVec
	consumed    *prometheus.CounterVec
	errors      *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	rate        *prometheus.GaugeVec
	capacity    *prometheus.GaugeVec
	mu          sync.Mutex
	policies    map[string]limiter.Config
	maxPolicies int
}

// NewPrometheus 返回可检查的注册错误，不使用 MustRegister 在应用启动时意外 panic。
// 同名采集器从 Registry 复用，不维护永久的全局 registry 缓存。
func NewPrometheus(next limiter.Limiter, cfg limiter.Config, opts Options) (limiter.DetailedLimiter, error) {
	if limiter.IsNil(next) {
		return nil, fmt.Errorf("metrics: nil limiter")
	}
	cfg, err := limiter.NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if provider, ok := next.(interface{ Configuration() limiter.Config }); ok && provider.Configuration() != cfg {
		return nil, fmt.Errorf("metrics: config does not match wrapped limiter")
	}
	if opts.PolicyName == "" {
		opts.PolicyName = "default"
	}
	if !regexp.MustCompile("^[a-zA-Z0-9_.-]{1,64}$").MatchString(opts.PolicyName) {
		return nil, fmt.Errorf("metrics: invalid fixed policy name")
	}
	if opts.Backend == "" {
		switch next.(type) {
		case *limiter.RedisLimiter:
			opts.Backend = "redis"
		case *limiter.LocalLimiter:
			opts.Backend = "local"
		default:
			opts.Backend = "custom"
		}
	}
	if opts.Backend != "redis" && opts.Backend != "local" && opts.Backend != "custom" {
		return nil, fmt.Errorf("metrics: backend must be redis/local/custom")
	}
	if opts.MaxPolicies == 0 {
		opts.MaxPolicies = 100
	}
	if opts.MaxPolicies < 1 {
		return nil, fmt.Errorf("metrics: invalid MaxPolicies")
	}
	if opts.Namespace == "" {
		opts.Namespace = "golimit"
	}
	if opts.Subsystem == "" {
		opts.Subsystem = "limiter"
	}
	registerer := opts.Registerer
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	c := newCollectors(opts)
	if err := registerer.Register(c); err != nil {
		var existing prometheus.AlreadyRegisteredError
		if !errors.As(err, &existing) {
			return nil, err
		}
		var ok bool
		c, ok = existing.ExistingCollector.(*collectors)
		if !ok {
			return nil, fmt.Errorf("metrics: conflicting collector")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxPolicies != opts.MaxPolicies {
		return nil, fmt.Errorf("metrics: inconsistent MaxPolicies for shared collectors")
	}
	if old, ok := c.policies[opts.PolicyName]; ok {
		if old != cfg {
			return nil, fmt.Errorf("metrics: policy already registered with different config")
		}
	} else {
		if len(c.policies) >= c.maxPolicies {
			return nil, fmt.Errorf("metrics: policy cardinality limit reached")
		}
		c.policies[opts.PolicyName] = cfg
	}
	c.rate.WithLabelValues(opts.PolicyName).Set(cfg.Rate)
	c.capacity.WithLabelValues(opts.PolicyName).Set(float64(cfg.Capacity))
	return &prometheusLimiter{next: next, cfg: cfg, policy: opts.PolicyName, backend: opts.Backend, collectors: c}, nil
}

// WrapPrometheus 保留旧入口，注册失败会 panic；新接入使用 NewPrometheus 处理 error。
// 返回详细接口，经过指标包装后 RetryAfter/Remaining 信息不会丢失。
func WrapPrometheus(next limiter.Limiter, cfg limiter.Config, opts Options) limiter.DetailedLimiter {
	l, err := NewPrometheus(next, cfg, opts)
	if err != nil {
		panic(err)
	}
	return l
}
func (p *prometheusLimiter) Configuration() limiter.Config { return p.cfg }
func (p *prometheusLimiter) Allow(ctx context.Context, key string, count int64) (bool, error) {
	r, err := p.Check(ctx, key, count)
	return r.Allowed, err
}
func (p *prometheusLimiter) Check(ctx context.Context, key string, count int64) (limiter.Result, error) {
	start := time.Now()
	// 即便自定义后端忘记校验，也不能让负成本导致 Prometheus Counter panic。
	var res limiter.Result
	err := limiter.ValidateRequest(key, count, p.cfg.Capacity)
	if err == nil {
		res, err = limiter.Check(ctx, p.next, key, count)
	}
	result := "allow"
	if err != nil {
		result = "error"
	} else if !res.Allowed {
		result = "deny"
	}
	c := p.collectors
	c.duration.WithLabelValues(p.policy, p.backend).Observe(time.Since(start).Seconds())
	c.checks.WithLabelValues(p.policy, p.backend, result).Inc()
	// 无效负成本不能加入 Counter；requested 表示请求需求，包含被拒绝的正成本。
	if count > 0 {
		c.requested.WithLabelValues(p.policy, p.backend).Add(float64(count))
	}
	// consumed 只记录明确成功扣减；结果未知的错误不伪造为零消耗或成功消耗。
	if err == nil && res.Allowed {
		c.consumed.WithLabelValues(p.policy, p.backend).Add(float64(count))
	}
	if err != nil {
		c.errors.WithLabelValues(p.policy, p.backend, errorCode(err)).Inc()
	}
	return res, err
}
func errorCode(err error) string {
	switch {
	case errors.Is(err, limiter.ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, limiter.ErrInvalidConfig):
		return "invalid_config"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, limiter.ErrLocalCapacity):
		return "local_capacity"
	default:
		return "backend"
	}
}

func newCollectors(o Options) *collectors {
	counter := func(name, help string, labels []string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: o.Namespace, Subsystem: o.Subsystem, Name: name, Help: help}, labels)
	}
	gauge := func(name, help string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: o.Namespace, Subsystem: o.Subsystem, Name: name, Help: help}, []string{"policy"})
	}
	return &collectors{
		checks:    counter("checks_total", "Limiter decisions by result.", []string{"policy", "backend", "result"}),
		requested: counter("requested_tokens_total", "Positive tokens requested, including denied or failed checks.", []string{"policy", "backend"}),
		consumed:  counter("consumed_tokens_total", "Tokens with confirmed successful deduction; unknown outcomes excluded.", []string{"policy", "backend"}),
		errors:    counter("errors_total", "Limiter errors by bounded error category.", []string{"policy", "backend", "error_code"}),
		duration:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: o.Namespace, Subsystem: o.Subsystem, Name: "check_duration_seconds", Help: "Duration of limiter checks.", Buckets: []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1, 2}}, []string{"policy", "backend"}),
		rate:      gauge("config_rate", "Configured tokens per second."),
		capacity:  gauge("config_capacity", "Configured bucket capacity."),
		policies:  make(map[string]limiter.Config), maxPolicies: o.MaxPolicies,
	}
}
func (c *collectors) Describe(ch chan<- *prometheus.Desc) {
	c.checks.Describe(ch)
	c.requested.Describe(ch)
	c.consumed.Describe(ch)
	c.errors.Describe(ch)
	c.duration.Describe(ch)
	c.rate.Describe(ch)
	c.capacity.Describe(ch)
}
func (c *collectors) Collect(ch chan<- prometheus.Metric) {
	c.checks.Collect(ch)
	c.requested.Collect(ch)
	c.consumed.Collect(ch)
	c.errors.Collect(ch)
	c.duration.Collect(ch)
	c.rate.Collect(ch)
	c.capacity.Collect(ch)
}

// HandlerFor 自定义 Registry 时必须传对应的 Gatherer，避免 /metrics 暴露空数据。
func Handler() http.Handler { return HandlerFor(prometheus.DefaultGatherer) }
func HandlerFor(gatherer prometheus.Gatherer) http.Handler {
	if gatherer == nil {
		gatherer = prometheus.DefaultGatherer
	}
	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})
}
