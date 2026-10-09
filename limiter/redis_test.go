package limiter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisClientSafetyValidation(t *testing.T) {
	for _, opts := range []*redis.Options{
		{Addr: "localhost:6379"},
		{Addr: "localhost:6379", ContextTimeoutEnabled: true},
		{Addr: "localhost:6379", MaxRetries: -1},
	} {
		c := redis.NewClient(opts)
		_, err := NewRedis(c, Config{Rate: 1, Capacity: 1})
		_ = c.Close()
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("unsafe client accepted: %v", err)
		}
	}
	c := redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{"localhost:6379"}, ContextTimeoutEnabled: true, MaxRetries: -1, MaxRedirects: -1})
	defer c.Close()
	if _, err := NewRedis(c, Config{Rate: 1, Capacity: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestRedisInvalidRequestsDoNotCreateKeys(t *testing.T) {
	c, s := testRedis(t)
	l, err := NewRedis(c, Config{Rate: 1, Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []struct {
		key   string
		count int64
	}{
		{"", 1}, {"x", 0}, {"x", -1}, {"x", 3}, {strings.Repeat("a", MaxKeyBytes+1), 1},
	} {
		if _, err := l.Check(context.Background(), req.key, req.count); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("request %+v: %v", req, err)
		}
	}
	if len(s.Keys()) != 0 {
		t.Fatalf("invalid requests created keys: %v", s.Keys())
	}
}

func TestConcurrentRedisDeductionsAndMetadata(t *testing.T) {
	c, _ := testRedis(t)
	l, err := NewRedis(c, Config{Rate: .0001, Capacity: 10, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var allowed atomic.Int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := l.Check(context.Background(), "shared", 1)
			if err != nil {
				t.Error(err)
				return
			}
			if r.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 10 {
		t.Fatalf("concurrent allow=%d, want 10", got)
	}
	r, err := l.Check(context.Background(), "shared", 1)
	if err != nil || r.Allowed || !r.Metadata || r.Remaining != 0 || r.RetryAfter <= 0 || r.ResetAfter < r.RetryAfter {
		t.Fatalf("metadata %+v %v", r, err)
	}
}

func TestBucketConfigConflictAndCorruptStateDoNotResetQuota(t *testing.T) {
	c, s := testRedis(t)
	ctx := context.Background()
	l, err := NewRedis(c, Config{Rate: 1, Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Check(ctx, "conflict", 2); err != nil {
		t.Fatal(err)
	}
	other, err := NewRedis(c, Config{Rate: 2, Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Check(ctx, "conflict", 1); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("config conflict: %v", err)
	}
	if got := s.HGet("golimit:conflict", "tokens"); got != "0" {
		t.Fatalf("conflict changed quota: %s", got)
	}
	if err := c.HSet(ctx, "golimit:corrupt", "tokens", "bad", "last_time", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Check(ctx, "corrupt", 1); !errors.Is(err, ErrBackend) {
		t.Fatalf("corrupt state: %v", err)
	}
	if got := s.HGet("golimit:corrupt", "tokens"); got != "bad" {
		t.Fatalf("corrupt bucket reset: %s", got)
	}
}

func testRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), ContextTimeoutEnabled: true, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return client, server
}
