package limiter

import (
	"context"
	"errors"
	"github.com/redis/go-redis/v9"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 故障注入代理先完成正常握手，再丢弃响应，真实验证 socket 截止时间。
// 故意让脚本在服务端执行成功，证明客户端超时不能当成“没有扣减”。
func TestRedisNetworkTimeoutAndCallerDeadline(t *testing.T) {
	_, server := testRedis(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var stalled atomic.Bool
	var connsMu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = listener.Close()
		connsMu.Lock()
		defer connsMu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			downstream, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", server.Addr())
			if err != nil {
				_ = downstream.Close()
				return
			}
			connsMu.Lock()
			conns = append(conns, downstream, upstream)
			connsMu.Unlock()
			go func() { _, _ = io.Copy(upstream, downstream) }()
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := upstream.Read(buf)
					if err != nil {
						return
					}
					if !stalled.Load() {
						if _, err = downstream.Write(buf[:n]); err != nil {
							return
						}
					}
				}
			}()
		}
	}()
	client := redis.NewClient(&redis.Options{Addr: listener.Addr().String(), ContextTimeoutEnabled: true, MaxRetries: -1, ReadTimeout: time.Second, WriteTimeout: time.Second, DisableIdentity: true, PoolSize: 1})
	defer client.Close()
	l, err := NewRedis(client, Config{Rate: 1, Capacity: 2, Timeout: 80 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := l.Check(ctx, "warm", 1); err != nil {
		t.Fatal(err)
	} // 加载脚本，避免 NOSCRIPT 回退干扰观察。
	stalled.Store(true)
	start := time.Now()
	_, err = l.Check(ctx, "unknown", 1)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrBackend) {
		t.Fatalf("timeout classification: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout ignored: %s", elapsed)
	}
	if got := server.HGet("golimit:unknown", "tokens"); got != "1" {
		t.Fatalf("script did not execute once: tokens=%s", got)
	}
	stalled.Store(false)
	// 超时连接已经丢弃，重新握手后再验证调用方较短的截止时间优先。
	if _, err := l.Check(ctx, "warm", 1); err != nil {
		t.Fatal(err)
	}
	stalled.Store(true)
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = l.Check(short, "caller", 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("caller deadline ignored: %s", elapsed)
	}
}
