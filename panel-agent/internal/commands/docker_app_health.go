package commands

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// loopbackPortRE matches a host port published to 127.0.0.1 in a rendered
// compose.yml, e.g. `"127.0.0.1:10000:80/tcp"`.
var loopbackPortRE = regexp.MustCompile(`127\.0\.0\.1:(\d+):`)

// probeLoopbackPorts returns the host ports an install publishes to 127.0.0.1.
// These are the app's reverse-proxied HTTP ports — DB / sidecar containers
// don't publish a host port (they talk over the compose network) — so an HTTP
// GET to one is a direct "is the app serving?" check that does NOT depend on
// the in-container docker healthcheck (which may call a tool the image lacks,
// like wget, or probe the wrong port). The whole catalog's healthcheck
// fragility class collapses to this one host-side probe.
func probeLoopbackPorts(dir string) []int {
	b, err := os.ReadFile(filepath.Join(dir, "compose.yml"))
	if err != nil {
		return nil
	}
	var out []int
	for _, m := range loopbackPortRE.FindAllStringSubmatch(string(b), -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// httpServing reports whether an HTTP GET to any of the ports returns a
// response with status < 500. A response of any kind (200/302/401/404) means
// the app is up and serving; connection-refused / timeout / 5xx means it is
// not. The GET hits "/" — a deliberately generic liveness path that returns
// <500 on every catalog app once it's up (even when it redirects to a login).
func httpServing(ctx context.Context, ports []int) bool {
	// A redirect is the answer, not a link to follow: an app that sends "/"
	// to https://<its domain>/login would otherwise send the probe to TLS on
	// a plain-HTTP port, and an installed Nextcloud never passed (GH #1956).
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, p := range ports {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", p), nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 500 {
			return true
		}
	}
	return false
}
