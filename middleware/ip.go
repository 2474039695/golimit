package middleware

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/2474039695/golimit/limiter"
)

// ClientIPKey 只在直连地址属于明确的代理网段时读取 X-Forwarded-For。
// 从右向左跳过受信任代理，遇到第一个非代理地址即停止，防止客户端伪造左侧 IP。
// 未配置代理时只用 RemoteAddr，不信任 X-Real-IP、Forwarded 等任意请求头。
// 此 helper 仅适用于代理会追加/覆盖 X-Forwarded-For 的部署。
func ClientIPKey(trustedProxies []netip.Prefix) (func(*http.Request) (string, error), error) {
	prefixes := append([]netip.Prefix(nil), trustedProxies...)
	for _, p := range prefixes {
		if !p.IsValid() {
			return nil, fmt.Errorf("%w: invalid trusted proxy prefix", limiter.ErrInvalidConfig)
		}
	}
	trusted := func(ip netip.Addr) bool {
		for _, p := range prefixes {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	}
	return func(r *http.Request) (string, error) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return "", fmt.Errorf("%w: invalid RemoteAddr", limiter.ErrInvalidRequest)
		}
		peer, err := netip.ParseAddr(host)
		if err != nil {
			return "", fmt.Errorf("%w: invalid peer address", limiter.ErrInvalidRequest)
		}
		peer = peer.Unmap()
		if !trusted(peer) {
			return peer.String(), nil
		}
		forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
		if forwarded == "" {
			return peer.String(), nil
		}
		// 限制代理链长度，避免任意长 header 使限流前的身份提取消耗过多 CPU/内存。
		if len(forwarded) > 4096 {
			return "", fmt.Errorf("%w: oversized proxy chain", limiter.ErrInvalidRequest)
		}
		chain := strings.Split(forwarded, ",")
		for i := len(chain) - 1; i >= 0; i-- {
			ip, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
			if err != nil {
				return "", fmt.Errorf("%w: invalid proxy chain", limiter.ErrInvalidRequest)
			}
			peer = ip.Unmap()
			if !trusted(peer) {
				return peer.String(), nil
			}
		}
		return peer.String(), nil
	}, nil
}
