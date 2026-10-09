package middleware

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/2474039695/golimit/limiter"
	"github.com/gogf/gf/v2/net/ghttp"
)

func TestGoFrameAdapterMatchesHTTPDecision(t *testing.T) {
	l, err := limiter.NewLocal(limiter.Config{Rate: .001, Capacity: 1}, limiter.LocalOptions{MaxKeys: 2})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewGoFrameWithOptions(l, Options{PolicyName: "gf", KeyFunc: func(r *http.Request) (string, error) { return r.Header.Get("X-Test-Identity"), nil }})
	if err != nil {
		t.Fatal(err)
	}
	s := ghttp.GetServer(fmt.Sprintf("golimit-test-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.SetErrorLogEnabled(false)
	s.Use(h)
	s.BindHandler("/", func(r *ghttp.Request) { r.Response.Write("ok") })
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	client := &http.Client{Timeout: 2 * time.Second}
	for _, want := range []int{200, 429} {
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", s.GetListenedPort()), nil)
		req.Header.Set("X-Test-Identity", "a")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("status=%d want=%d body=%s", res.StatusCode, want, body)
		}
		if want == 429 && res.Header.Get("Retry-After") == "" {
			t.Fatal("GoFrame missing Retry-After")
		}
	}
}
