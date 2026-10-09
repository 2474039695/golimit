package limiter

import (
	"container/heap"
	"context"
	"math"
	"sync"
	"time"
)

// LocalOptions 约束本地兜底的内存。每个进程拥有独立配额，不能替代全局 Redis 配额。
type LocalOptions struct {
	MaxKeys int // 必须显式设置，防止故障期间无限创建身份桶。
}
type localBucket struct {
	key     string
	index   int
	tokens  float64
	updated time.Time
	expires time.Time
}

// LocalLimiter 是并发安全的本地令牌桶。满槽时只清理过期桶，不淘汰仍有效的桶：
// 淘汰一个未补满的桶再重新创建，会让用户通过轮换身份反复获得初始满额。
type LocalLimiter struct {
	mu          sync.Mutex
	cfg         Config
	maxKeys     int
	ttl         time.Duration
	buckets     map[string]*localBucket
	expirations expiryHeap
}

func NewLocal(cfg Config, opts LocalOptions) (*LocalLimiter, error) {
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if opts.MaxKeys < 1 {
		return nil, ErrInvalidConfig
	}
	return &LocalLimiter{cfg: cfg, maxKeys: opts.MaxKeys, ttl: time.Duration(ttlSeconds(cfg)) * time.Second, buckets: make(map[string]*localBucket)}, nil
}
func (l *LocalLimiter) Configuration() Config { return l.cfg }
func (l *LocalLimiter) Allow(ctx context.Context, key string, count int64) (bool, error) {
	r, err := l.Check(ctx, key, count)
	return r.Allowed, err
}

// Check 使用 Go 单调时钟计算间隔，不受本机墙上时钟校正影响。
func (l *LocalLimiter) Check(ctx context.Context, key string, count int64) (Result, error) {
	if err := ValidateRequest(key, count, l.cfg.Capacity); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	now := time.Now()
	b, ok := l.buckets[key]
	if ok && !now.Before(b.expires) {
		heap.Remove(&l.expirations, b.index)
		delete(l.buckets, key)
		ok = false
	}
	if !ok {
		if len(l.buckets) >= l.maxKeys {
			// 最小堆只访问已过期的桶，满槽时不会每次扫描所有身份。
			for len(l.expirations) > 0 && !now.Before(l.expirations[0].expires) {
				expired := heap.Pop(&l.expirations).(*localBucket)
				delete(l.buckets, expired.key)
			}
			if len(l.buckets) >= l.maxKeys {
				return Result{}, ErrLocalCapacity
			}
		}
		b = &localBucket{key: key, tokens: float64(l.cfg.Capacity), updated: now, expires: now.Add(l.ttl)}
		l.buckets[key] = b
		heap.Push(&l.expirations, b)
	}
	b.tokens = math.Min(float64(l.cfg.Capacity), b.tokens+now.Sub(b.updated).Seconds()*l.cfg.Rate)
	b.updated = now
	b.expires = now.Add(l.ttl)
	heap.Fix(&l.expirations, b.index)
	allowed := b.tokens >= float64(count)
	retry := time.Duration(0)
	reason := "allow"
	if allowed {
		b.tokens -= float64(count)
	} else {
		retry = waitDuration(float64(count)-b.tokens, l.cfg.Rate)
		reason = "rate_limited"
	}
	return Result{Allowed: allowed, Remaining: int64(math.Floor(b.tokens)), Capacity: l.cfg.Capacity, RetryAfter: retry, ResetAfter: waitDuration(float64(l.cfg.Capacity)-b.tokens, l.cfg.Rate), Reason: reason, Metadata: true}, nil
}

// 堆中每个桶只有一个节点，更新过期时间不会留下无限增长的旧记录。
type expiryHeap []*localBucket

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].expires.Before(h[j].expires) }
func (h expiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *expiryHeap) Push(x any)        { b := x.(*localBucket); b.index = len(*h); *h = append(*h, b) }
func (h *expiryHeap) Pop() any {
	old := *h
	n := len(old)
	b := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	b.index = -1
	return b
}
func waitDuration(tokens, rate float64) time.Duration {
	if tokens <= 0 {
		return 0
	}
	return time.Duration(math.Ceil(tokens / rate * float64(time.Second)))
}
