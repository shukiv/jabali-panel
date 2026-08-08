package hostedsvc

import (
	"net"
	"net/http"
)

// ClientIP resolves the request's true source address. The service runs on
// loopback behind the box's nginx, so the ONLY trusted forwarding hop is the
// local proxy: X-Real-IP is honored strictly when the TCP peer is loopback,
// and client-supplied X-Forwarded-For is never consulted. A forged header on
// a direct external connection must not let a caller choose an arbitrary
// label — that would resurrect the squatting/rebinding surface the
// server-side IP derivation exists to kill (blueprint trap 2). The claim
// path must also never sit behind a CDN, or every registrant "is" a CDN IP.
func ClientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return nil
	}
	if peer.IsLoopback() {
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			if ip := net.ParseIP(xr); ip != nil {
				return ip
			}
		}
	}
	return peer
}
