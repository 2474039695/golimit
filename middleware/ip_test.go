package middleware

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestTrustedProxyIPCannotBeSpoofed(t *testing.T) {
	key, err := ClientIPKey([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ peer, xff, want string }{
		{"203.0.113.5:1234", "198.51.100.1", "203.0.113.5"},
		{"10.0.0.1:1234", "198.51.100.1, 203.0.113.5, 10.0.0.2", "203.0.113.5"},
		{"10.0.0.1:1234", "203.0.113.5", "203.0.113.5"},
		{"[::ffff:203.0.113.5]:1234", "198.51.100.1", "203.0.113.5"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.xff)
		got, err := key(r)
		if err != nil || got != tc.want {
			t.Fatalf("%+v got=%s err=%v", tc, got, err)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "not-an-ip")
	if _, err := key(r); err == nil {
		t.Fatal("invalid trusted proxy chain accepted")
	}
}
