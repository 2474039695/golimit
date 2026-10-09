package limiter

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestScriptPreservesPartialRefillOnDeniedRequests(t *testing.T) {
	client, _ := testRedis(t)
	ctx := context.Background()
	check := func(now int64, want int) {
		t.Helper()
		got, err := redis.NewScript(luaScript).Run(ctx, client, []string{"partial"}, 10, 1, now, 1, 604800, 604800).Int64Slice()
		if err != nil {
			t.Fatal(err)
		}
		if got[0] != int64(want) {
			t.Fatalf("at %d ms: got %d, want %d", now, got, want)
		}
	}

	check(0, 1)
	check(50, 0)
	check(100, 1)
	check(150, 0)
	check(200, 1)
}

func TestScriptDiscardsExcessTimeAtCapacity(t *testing.T) {
	client, _ := testRedis(t)
	ctx := context.Background()
	for _, step := range []struct {
		now  int64
		want int
	}{
		{0, 1},
		{10000, 1},
		{10000, 0},
		{10500, 0},
		{11000, 1},
	} {
		got, err := redis.NewScript(luaScript).Run(ctx, client, []string{"full"}, 1, 1, step.now, 1, 604800, 604800).Int64Slice()
		if err != nil {
			t.Fatal(err)
		}
		if got[0] != int64(step.want) {
			t.Fatalf("at %d ms: got %d, want %d", step.now, got, step.want)
		}
	}
}

func TestScriptCarriesFractionalPeriodsBelowCapacity(t *testing.T) {
	client, _ := testRedis(t)
	ctx := context.Background()
	for _, step := range []struct {
		now       int64
		requested int
		want      int
	}{
		{0, 2, 1},
		{333, 1, 0},
		{334, 1, 1},
		{666, 1, 0},
		{667, 1, 1},
	} {
		got, err := redis.NewScript(luaScript).Run(ctx, client, []string{"carry"}, 3, 2, step.now, step.requested, 604800, 604800).Int64Slice()
		if err != nil {
			t.Fatal(err)
		}
		if got[0] != int64(step.want) {
			t.Fatalf("at %d ms: got %d, want %d", step.now, got, step.want)
		}
	}
}

func TestScriptFractionalRateAndClockRollback(t *testing.T) {
	client, _ := testRedis(t)
	ctx := context.Background()
	for _, step := range []struct {
		now  int64
		want int
	}{
		{1000, 1},
		{900, 0},
		{1333, 0},
		{1334, 1},
		{1667, 0},
		{1668, 1},
		{2001, 0},
		{2002, 1},
	} {
		got, err := redis.NewScript(luaScript).Run(ctx, client, []string{"fractional"}, 3, 1, step.now, 1, 604800, 604800).Int64Slice()
		if err != nil {
			t.Fatal(err)
		}
		if got[0] != int64(step.want) {
			t.Fatalf("at %d ms: got %d, want %d", step.now, got, step.want)
		}
	}
}

func TestScriptKeepsSlowBucketsUntilTheyRefill(t *testing.T) {
	client, server := testRedis(t)
	ctx := context.Background()
	if _, err := redis.NewScript(luaScript).Run(ctx, client, []string{"slow"}, 0.0001, 1, 0, 1, 604800, 604800).Int64Slice(); err != nil {
		t.Fatal(err)
	}
	if ttl := server.TTL("slow"); ttl < 10000*time.Second {
		t.Fatalf("TTL %s expires before the bucket refills", ttl)
	}
}

func TestScriptReadsExistingBucketWithoutRemainingTime(t *testing.T) {
	client, _ := testRedis(t)
	ctx := context.Background()
	if err := client.HSet(ctx, "legacy", "tokens", 0, "last_time", 0).Err(); err != nil {
		t.Fatal(err)
	}
	got, err := redis.NewScript(luaScript).Run(ctx, client, []string{"legacy"}, 10, 1, 100, 1, 604800, 604800).Int64Slice()
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 1 {
		t.Fatalf("existing bucket: got %d, want 1", got)
	}
}

func TestScriptRejectsInvalidArgumentsWithoutWritingState(t *testing.T) {
	client, server := testRedis(t)
	ctx := context.Background()
	for _, args := range [][]any{
		{0, 1, 0, 1},
		{1, 0, 0, 1},
		{1, 1, 0, 0},
		{1, 1, 0, -1},
		{1e-20, 1, 0, 1},
	} {
		if err := redis.NewScript(luaScript).Run(ctx, client, []string{"invalid"}, append(args, 604800, 604800)...).Err(); err == nil {
			t.Fatalf("arguments %v were accepted", args)
		}
		if server.Exists("invalid") {
			t.Fatalf("arguments %v wrote limiter state", args)
		}
	}
}
