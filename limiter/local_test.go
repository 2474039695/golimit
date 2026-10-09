package limiter

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLocalBoundedMemoryAndNoActiveEviction(t *testing.T) {
	l, err := NewLocal(Config{Rate: .001, Capacity: 1}, LocalOptions{MaxKeys: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if r, err := l.Check(ctx, "a", 1); err != nil || !r.Allowed {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := l.Check(ctx, "b", 1); !errors.Is(err, ErrLocalCapacity) {
		t.Fatalf("capacity: %v", err)
	}
	if r, err := l.Check(ctx, "a", 1); err != nil || r.Allowed {
		t.Fatalf("active bucket reset: %+v %v", r, err)
	}
	l.mu.Lock()
	l.buckets["a"].expires = time.Now().Add(-time.Second)
	l.mu.Unlock()
	if r, err := l.Check(ctx, "b", 1); err != nil || !r.Allowed {
		t.Fatalf("expired slot: %+v %v", r, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := l.Check(canceled, "b", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
