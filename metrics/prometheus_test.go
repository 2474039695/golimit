package metrics

import (
	"context"
	"errors"
	"fmt"
	"github.com/2474039695/golimit/limiter"
	"github.com/prometheus/client_golang/prometheus"
	"testing"
)

type metricStub struct{ calls int }

func (s *metricStub) Allow(ctx context.Context, k string, n int64) (bool, error) {
	r, e := s.Check(ctx, k, n)
	return r.Allowed, e
}
func (s *metricStub) Check(_ context.Context, _ string, _ int64) (limiter.Result, error) {
	s.calls++
	switch s.calls {
	case 1:
		return limiter.Result{Allowed: true, Remaining: 3, Capacity: 4, Metadata: true}, nil
	case 2:
		return limiter.Result{Allowed: false, Remaining: 3, Capacity: 4, Metadata: true}, nil
	default:
		return limiter.Result{}, errors.New("unknown outcome")
	}
}

func TestRequestedConsumedAndMetadata(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := NewPrometheus(&metricStub{}, limiter.Config{Rate: 1, Capacity: 4}, Options{Registerer: reg, PolicyName: "login"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r, _ := l.Check(context.Background(), fmt.Sprintf("user:%d", i), 2)
		if i == 0 && (!r.Metadata || r.Remaining != 3) {
			t.Fatalf("wrapper lost metadata: %+v", r)
		}
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, f := range families {
		for _, m := range f.Metric {
			for _, label := range m.Label {
				if label.GetName() == "key" {
					t.Fatal("raw identity exposed as metric label")
				}
			}
			if m.Counter != nil {
				values[f.GetName()] += m.Counter.GetValue()
			}
		}
	}
	if values["golimit_limiter_requested_tokens_total"] != 6 || values["golimit_limiter_consumed_tokens_total"] != 2 || values["golimit_limiter_errors_total"] != 1 {
		t.Fatalf("wrong counter semantics: %v", values)
	}
}

func TestDynamicKeysDoNotIncreaseSeriesAndPolicyBudget(t *testing.T) {
	reg := prometheus.NewRegistry()
	cfg := limiter.Config{Rate: 1, Capacity: 1}
	// bool-only 旧接口也能包装，但不能生成详细元数据。
	l, err := NewPrometheus(alwaysAllow{}, cfg, Options{Registerer: reg, PolicyName: "fixed", MaxPolicies: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if _, err := l.Allow(context.Background(), fmt.Sprintf("user:%d", i), 1); err != nil {
			t.Fatal(err)
		}
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if len(f.Metric) != 1 {
			t.Fatalf("dynamic identities expanded %s to %d series", f.GetName(), len(f.Metric))
		}
	}
	if _, err := NewPrometheus(alwaysAllow{}, cfg, Options{Registerer: reg, PolicyName: "second", MaxPolicies: 1}); err == nil {
		t.Fatal("policy budget ignored")
	}
	if _, err := NewPrometheus(alwaysAllow{}, cfg, Options{Registerer: reg, PolicyName: "fixed", MaxPolicies: 1}); err != nil {
		t.Fatalf("collector reuse failed: %v", err)
	}
}

type alwaysAllow struct{}

func (alwaysAllow) Allow(context.Context, string, int64) (bool, error) { return true, nil }
