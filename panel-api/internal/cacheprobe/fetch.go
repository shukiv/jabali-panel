package cacheprobe

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Probe fetches host's homepage twice, anonymously, and classifies the result.
//
// Anonymously is the point: a logged-in cookie or an Authorization header makes
// nginx bypass the cache by design, so probing as anyone but a fresh visitor
// would report BYPASS on a perfectly healthy site.
//
// The request goes to the loopback address with the Host header set, so it
// exercises this box's own vhost rather than wherever public DNS currently
// points — a pre-cutover domain still resolves to the old host, and probing that
// would report on somebody else's server.
func Probe(ctx context.Context, host string) (Result, error) {
	cl := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			// Pin every connection to loopback regardless of DNS.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, "127.0.0.1:443")
			},
			// The origin certificate may legitimately be the self-signed
			// placeholder on a not-yet-cut-over domain. We are reading cache
			// headers from our own box over loopback, not authenticating a peer.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402
		},
		// Cache status is a property of the response we asked for; following a
		// redirect would report on a different URL than the operator sees.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	get := func() (http.Header, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/", nil)
		if err != nil {
			return nil, err
		}
		req.Host = host
		req.Header.Set("User-Agent", "jabali-cache-probe/1")
		// Ask for an uncompressed body so Vary handling is not muddied.
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := cl.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return resp.Header, nil
	}

	first, err := get()
	if err != nil {
		return Result{Verdict: VerdictUnknown, Reason: "probe request failed: " + err.Error()},
			fmt.Errorf("cache probe %s: %w", host, err)
	}
	// A cacheable response has to be stored before it can be served, so the
	// second fetch is the one that proves anything.
	second, err := get()
	if err != nil {
		return Result{Verdict: VerdictUnknown, Reason: "second probe request failed: " + err.Error()},
			fmt.Errorf("cache probe %s: %w", host, err)
	}
	return Classify(first, second), nil
}
