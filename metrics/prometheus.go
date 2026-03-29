package metrics

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/2474039695/golimit/limiter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	defaultNamespace = "golimit"
	defaultSubsystem = "limiter"
)

// Options controls Prometheus metric registration.
type Options struct {
	Namespace  string
	Subsystem  string
	Registerer prometheus.Registerer
}

type prometheusLimiter struct {
	next       limiter.Limiter
	cfg        limiter.Config
	collectors *collectors
}

type collectors struct {
	requestsTotal  *prometheus.CounterVec
	requestTokens  *prometheus.CounterVec
	allowDuration  *prometheus.HistogramVec
	configRate     *prometheus.GaugeVec
	configCapacity *prometheus.GaugeVec
}

type collectorKey struct {
	namespace  string
	subsystem  string
	registerer prometheus.Registerer
}

var (
	collectorsMu    sync.Mutex
	collectorsByKey = make(map[collectorKey]*collectors)
)

// WrapPrometheus wraps a limiter with Prometheus metrics.
func WrapPrometheus(next limiter.Limiter, cfg limiter.Config, opts Options) limiter.Limiter {
	if next == nil {
		panic("metrics: next limiter must not be nil")
	}

	return &prometheusLimiter{
		next:       next,
		cfg:        cfg,
		collectors: loadCollectors(opts),
	}
}

// Handler returns an HTTP handler backed by the default Prometheus gatherer.
func Handler() http.Handler {
	return HandlerFor(prometheus.DefaultGatherer)
}

// HandlerFor returns an HTTP handler backed by the provided Prometheus gatherer.
func HandlerFor(gatherer prometheus.Gatherer) http.Handler {
	if gatherer == nil {
		gatherer = prometheus.DefaultGatherer
	}

	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})
}

func (p *prometheusLimiter) Allow(ctx context.Context, key string, count int64) (bool, error) {
	start := time.Now()

	allowed, err := p.next.Allow(ctx, key, count)

	p.collectors.allowDuration.WithLabelValues(key).Observe(time.Since(start).Seconds())
	p.collectors.configRate.WithLabelValues(key).Set(p.cfg.Rate)
	p.collectors.configCapacity.WithLabelValues(key).Set(float64(p.cfg.Capacity))

	result := "allow"
	if err != nil {
		result = "error"
	} else if !allowed {
		result = "deny"
	}

	p.collectors.requestsTotal.WithLabelValues(key, result).Inc()
	p.collectors.requestTokens.WithLabelValues(key, result).Add(float64(count))

	return allowed, err
}

func loadCollectors(opts Options) *collectors {
	namespace := opts.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}

	subsystem := opts.Subsystem
	if subsystem == "" {
		subsystem = defaultSubsystem
	}

	registerer := opts.Registerer
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}

	key := collectorKey{
		namespace:  namespace,
		subsystem:  subsystem,
		registerer: registerer,
	}

	collectorsMu.Lock()
	defer collectorsMu.Unlock()

	if collectors, ok := collectorsByKey[key]; ok {
		return collectors
	}

	collectors := &collectors{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "requests_total",
				Help:      "Total number of limiter requests grouped by result.",
			},
			[]string{"key", "result"},
		),
		requestTokens: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "request_tokens_total",
				Help:      "Total requested tokens grouped by result.",
			},
			[]string{"key", "result"},
		),
		allowDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "allow_duration_seconds",
				Help:      "Duration of limiter checks in seconds.",
				Buckets:   []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1},
			},
			[]string{"key"},
		),
		configRate: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "config_rate",
				Help:      "Configured token generation rate for the limiter key.",
			},
			[]string{"key"},
		),
		configCapacity: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "config_capacity",
				Help:      "Configured token bucket capacity for the limiter key.",
			},
			[]string{"key"},
		),
	}

	registerer.MustRegister(
		collectors.requestsTotal,
		collectors.requestTokens,
		collectors.allowDuration,
		collectors.configRate,
		collectors.configCapacity,
	)

	collectorsByKey[key] = collectors
	return collectors
}
