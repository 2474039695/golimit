package limiter

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
)

// Config 属于一个规则；同一 Redis key 必须使用相同配置。
// Timeout、KeyPrefix、IdleTTL、MaxTTL 的零值使用默认值；负值报错。
type Config struct {
	Rate      float64       // 每秒令牌数，必须有限且大于零。
	Capacity  int64         // 正整数容量，不得超过 Lua 精确整数范围。
	KeyPrefix string        // 默认 golimit，最多 128 字节。
	Timeout   time.Duration // 默认 1 秒，调用方更短的 deadline 优先。
	IdleTTL   time.Duration // 默认 1 小时，桶空闲多久后清理。
	MaxTTL    time.Duration // 默认 7 天；不足以覆盖补满周期则报错，不截断恢复周期。
}

const (
	MaxKeyBytes          = 512
	maxSafeInteger int64 = 1<<53 - 1
	defaultMaxTTL        = 7 * 24 * time.Hour
)

// NormalizeConfig 统一补齐默认值和校验，保证各后端及指标使用同一配置语义。
func NormalizeConfig(cfg Config) (Config, error) {
	bad := func(msg string) (Config, error) { return Config{}, fmt.Errorf("%w: %s", ErrInvalidConfig, msg) }
	if cfg.Rate <= 0 || math.IsNaN(cfg.Rate) || math.IsInf(cfg.Rate, 0) {
		return bad("Rate must be finite and positive")
	}
	if cfg.Capacity < 1 || cfg.Capacity > maxSafeInteger {
		return bad("Capacity must be between 1 and 2^53-1")
	}
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = "golimit"
	}
	if validateName(cfg.KeyPrefix, 128) != nil {
		return bad("invalid KeyPrefix")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = time.Second
	}
	if cfg.IdleTTL == 0 {
		cfg.IdleTTL = time.Hour
	}
	if cfg.MaxTTL == 0 {
		cfg.MaxTTL = defaultMaxTTL
	}
	if cfg.Timeout < 0 || cfg.IdleTTL < time.Second || cfg.MaxTTL < time.Second {
		return bad("Timeout must be positive; TTLs must be at least one second")
	}
	// 先用浮点秒比较，避免极小 Rate 换算成 time.Duration 时溢出。
	ttl := math.Max(math.Ceil(cfg.IdleTTL.Seconds()), math.Ceil(float64(cfg.Capacity)/cfg.Rate)+1)
	if math.IsInf(ttl, 0) || ttl > math.Floor(cfg.MaxTTL.Seconds()) {
		return bad("MaxTTL must cover IdleTTL and full refill period plus one second")
	}
	return cfg, nil
}
func ttlSeconds(cfg Config) int64 {
	return int64(math.Max(math.Ceil(cfg.IdleTTL.Seconds()), math.Ceil(float64(cfg.Capacity)/cfg.Rate)+1))
}

// ValidateRequest 在访问存储前拒绝非法输入；超出桶容量的成本永远无法满足。
func ValidateRequest(key string, count, capacity int64) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if count < 1 || count > capacity {
		return fmt.Errorf("%w: count must be between 1 and Capacity", ErrInvalidRequest)
	}
	return nil
}

// ValidateKey 限制长度并拒绝空白、控制字符，避免动态输入产生任意大的键。
func ValidateKey(key string) error {
	if validateName(key, MaxKeyBytes) != nil {
		return fmt.Errorf("%w: invalid key (max %d bytes)", ErrInvalidRequest, MaxKeyBytes)
	}
	return nil
}
func validateName(s string, maxBytes int) error {
	if s == "" || len(s) > maxBytes || strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return ErrInvalidRequest
	}
	return nil
}
