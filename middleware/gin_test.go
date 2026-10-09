package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2474039695/golimit/limiter"
	"github.com/gin-gonic/gin"
)

type stubLimiter struct {
	check func(context.Context, string, int64) (limiter.Result, error)
}

func (s stubLimiter) Allow(ctx context.Context, k string, n int64) (bool, error) {
	r, e := s.Check(ctx, k, n)
	return r.Allowed, e
}
func (s stubLimiter) Check(ctx context.Context, k string, n int64) (limiter.Result, error) {
	return s.check(ctx, k, n)
}

func TestGinDynamicKeysWeightsAndHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l, err := limiter.NewLocal(limiter.Config{Rate: .001, Capacity: 2}, limiter.LocalOptions{MaxKeys: 10})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewGinWithOptions(l, Options{PolicyName: "batch", KeyFunc: func(r *http.Request) (string, error) { return r.Header.Get("X-Test-Identity"), nil }, CostFunc: func(*http.Request) (int64, error) { return 2, nil }})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(h)
	router.GET("/", func(c *gin.Context) { c.String(200, "ok") })
	for _, tc := range []struct {
		identity string
		status   int
	}{{"a", 200}, {"a", 429}, {"b", 200}, {"", 400}} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Test-Identity", tc.identity)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != tc.status {
			t.Fatalf("%s: status=%d body=%s", tc.identity, out.Code, out.Body.String())
		}
		if out.Code == 429 && (out.Header().Get("Retry-After") == "" || out.Header().Get("X-RateLimit-Remaining") != "0") {
			t.Fatalf("missing denial metadata: %v", out.Header())
		}
	}
}

func TestGinFailurePoliciesAndInvalidCost(t *testing.T) {
	failure := stubLimiter{check: func(context.Context, string, int64) (limiter.Result, error) {
		return limiter.Result{}, &limiter.BackendError{Cause: errors.New("connection lost")}
	}}
	var observed atomic.Int64
	for _, tc := range []struct {
		name   string
		policy ErrorPolicy
		want   []int
	}{
		{"closed", FailClosed, []int{503, 503}}, {"open", FailOpen, []int{200, 200}}, {"local", LocalFallback, []int{200, 429}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, err := limiter.NewLocal(limiter.Config{Rate: .001, Capacity: 1}, limiter.LocalOptions{MaxKeys: 1})
			if err != nil {
				t.Fatal(err)
			}
			h, err := NewGinWithOptions(failure, Options{PolicyName: "login", ErrorPolicy: tc.policy, Fallback: local, OnError: func(*http.Request, error) { observed.Add(1) }})
			if err != nil {
				t.Fatal(err)
			}
			r := gin.New()
			r.Use(h)
			r.GET("/", func(c *gin.Context) { c.Status(200) })
			for _, want := range tc.want {
				out := httptest.NewRecorder()
				r.ServeHTTP(out, httptest.NewRequest("GET", "/", nil))
				if out.Code != want {
					t.Fatalf("status=%d want=%d", out.Code, want)
				}
				if want == 503 && out.Header().Get("Retry-After") != "" {
					t.Fatal("backend error pretended to be quota exhaustion")
				}
			}
		})
	}
	if observed.Load() != 6 {
		t.Fatalf("error observation count=%d", observed.Load())
	}
	var called atomic.Bool
	h, err := NewGinWithOptions(stubLimiter{check: func(context.Context, string, int64) (limiter.Result, error) {
		called.Store(true)
		return limiter.Result{Allowed: true}, nil
	}},
		Options{PolicyName: "invalid", ErrorPolicy: FailOpen, CostFunc: func(*http.Request) (int64, error) { return -1, nil }})
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(h)
	r.GET("/", func(c *gin.Context) { c.Status(200) })
	out := httptest.NewRecorder()
	r.ServeHTTP(out, httptest.NewRequest("GET", "/", nil))
	if out.Code != 400 || called.Load() {
		t.Fatalf("invalid cost reached backend: %d %v", out.Code, called.Load())
	}
}

func TestGinSkipAndTotalBudget(t *testing.T) {
	slow := stubLimiter{check: func(ctx context.Context, _ string, _ int64) (limiter.Result, error) {
		<-ctx.Done()
		return limiter.Result{}, ctx.Err()
	}}
	h, err := NewGinWithOptions(slow, Options{PolicyName: "slow", Timeout: 10 * time.Millisecond, ErrorPolicy: FailOpen, SkipFunc: func(r *http.Request) bool { return r.URL.Path == "/health" }})
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(h)
	r.GET("/", func(c *gin.Context) { c.Status(200) })
	r.GET("/health", func(c *gin.Context) { c.Status(200) })
	for _, tc := range []struct {
		path string
		want int
	}{{"/", 503}, {"/health", 200}} {
		out := httptest.NewRecorder()
		r.ServeHTTP(out, httptest.NewRequest("GET", tc.path, nil))
		if out.Code != tc.want {
			t.Fatalf("%s status=%d", tc.path, out.Code)
		}
	}
}
