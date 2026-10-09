package limiter

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestConfigValidationAndTTL(t *testing.T) {
	for _, cfg := range []Config{
		{Rate: 0, Capacity: 1}, {Rate: math.NaN(), Capacity: 1}, {Rate: math.Inf(1), Capacity: 1},
		{Rate: 1, Capacity: 0}, {Rate: 1, Capacity: 1, Timeout: -1},
		{Rate: 1, Capacity: 1, KeyPrefix: "bad prefix"},
		{Rate: .0001, Capacity: 1, MaxTTL: time.Hour},
		{Rate: 1, Capacity: 1, IdleTTL: 2 * time.Hour, MaxTTL: time.Hour},
	} {
		if _, err := NormalizeConfig(cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("config %+v: %v", cfg, err)
		}
	}
	cfg, err := NormalizeConfig(Config{Rate: .0001, Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := ttlSeconds(cfg); got != 10001 {
		t.Fatalf("slow bucket TTL=%d", got)
	}
	cfg, err = NormalizeConfig(Config{Rate: 1, Capacity: 1, IdleTTL: time.Second, MaxTTL: 2 * time.Second})
	if err != nil || ttlSeconds(cfg) != 2 {
		t.Fatalf("boundary TTL: %+v %v", cfg, err)
	}
}
