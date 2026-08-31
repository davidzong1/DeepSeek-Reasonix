package cli

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newIPv4TestServer avoids httptest's IPv6 fallback, which is unavailable in
// restricted sandboxes. A missing loopback capability skips the network test;
// production code and normal test environments retain the same HTTP coverage.
func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: handler}}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// newTestProxy wraps httptest.NewServer for cli_test.go's proxy cases. It lives
// here because that file is over the size ceiling, where every line is recorded
// debt — and this helper file already imports httptest.
func newTestProxy(h http.HandlerFunc) *httptest.Server { return httptest.NewServer(h) }
