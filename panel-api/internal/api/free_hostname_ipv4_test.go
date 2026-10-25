package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The free-hostname service labels a box by its IPv4 source address and
// refuses an IPv6 caller, so the production client must never reach it over
// IPv6: a dual-stack box got its email code and then had every claim refused
// ("only IPv4 sources are supported in v1"). Uses the package client as built,
// before any test swaps it.
func TestFreeHostnameClientDialsIPv4Only(t *testing.T) {
	client := freeHostnameHTTP
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	ln6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	srv6 := &httptest.Server{Listener: ln6, Config: &http.Server{Handler: ok}}
	srv6.Start()
	defer srv6.Close()
	if resp, err := client.Get(srv6.URL); err == nil {
		resp.Body.Close()
		t.Fatalf("client reached an IPv6-only server (%s): HTTP %d", srv6.URL, resp.StatusCode)
	}

	srv4 := httptest.NewServer(ok)
	defer srv4.Close()
	resp, err := client.Get(srv4.URL)
	if err != nil {
		t.Fatalf("client could not reach an IPv4 server: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("IPv4 server: HTTP %d", resp.StatusCode)
	}
}
