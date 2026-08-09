package middleware

import (
	"net"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// allowTCPInternalRoutes reports whether internal routes may be served over a
// TCP listener, where the kernel gives us no peer identity.
//
// Opt-in on purpose, and read per call rather than cached at init so a test
// (and an operator restarting with the variable set) sees the current value.
// The default is refuse: production binds the unix socket, and silently
// trusting loopback on a multi-tenant host is exactly the exposure this gate
// exists to prevent.
func allowTCPInternalRoutes() bool {
	return os.Getenv("JABALI_ALLOW_TCP_INTERNAL") == "1"
}

// RequireLocalhost gates the routes only a local daemon (the agent) may call —
// /api/v1/internal/* and the malware ingest — so the SPA and the public
// internet can never reach them.
//
// The gate is layered, strongest first:
//
//  1. Proxy headers (X-Forwarded-*, X-Real-IP) are positive proof the request
//     came through nginx, which shares the same unix socket the agent uses.
//     Their presence is an immediate reject.
//  2. SO_PEERCRED: ask the kernel which uid opened the connection. Set at
//     connect time, unforgeable, unproxyable. root passes, anything else (nginx
//     is www-data) is rejected. This is the real check.
//  3. No peer credentials means a TCP listener. Loopback is NOT a trust
//     boundary on a multi-tenant box — every tenant process can bind it — so
//     these are refused unless the operator opts in via
//     JABALI_ALLOW_TCP_INTERNAL=1. Only then does the loopback address check
//     below apply.
func RequireLocalhost() gin.HandlerFunc {
	return func(c *gin.Context) {
		// A request that arrived through nginx is NOT a local caller, even
		// though it reaches us over the same unix socket the agent uses.
		//
		// This was exploitable: panel-api is fronted by nginx over
		// /run/jabali-panel/api.sock (ADR-0050), so EVERY proxied request has
		// RemoteAddr "@" and the unix-socket branch below accepted it
		// unconditionally. The /api/v1/internal/* and malware-ingest routes,
		// whose only gate is this middleware, were therefore reachable
		// unauthenticated from the public internet.
		//
		// nginx always sets these on proxied requests; the agent dialling the
		// socket directly never does. Their presence is positive proof the
		// request came through the proxy, so reject rather than trust the
		// socket. install/nginx also denies these paths outright -- this is the
		// half that does not depend on the vhost staying correct.
		for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "X-Forwarded-Proto", "X-Forwarded-Host"} {
			if c.GetHeader(h) != "" {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "localhost_only"})
				return
			}
		}

		// Ask the kernel who actually opened this socket. SO_PEERCRED is set
		// at connect time and cannot be forged or proxied, so this is the
		// structural answer to "is this really the agent?" -- nginx runs as
		// www-data and can never satisfy it.
		if uid, ok := PeerUID(c); ok {
			if uid != rootUID {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "localhost_only"})
				return
			}
			c.Next()
			return
		}

		// No peer credentials. Two very different cases hide here, and they
		// must not be treated alike.
		remote := c.Request.RemoteAddr

		// (a) Unix socket whose credentials we could not read — RemoteAddr is
		// "@" or "" (net/http sets no address for unix conns; some adapters
		// drop the "@" sentinel). Go ALWAYS sets a real host:port for TCP, so
		// a remote attacker cannot manufacture this case: it really is a local
		// unix peer. Accept, as before. This keeps the agent working if
		// ConnContext is ever unwired by a refactor.
		if remote == "" || remote == "@" {
			c.Next()
			return
		}

		// (b) A real TCP peer. Loopback is NOT a trust boundary on a
		// multi-tenant box — every local process (a tenant's PHP-FPM pool,
		// their cron jobs, an SSH shell) can connect to 127.0.0.1. Accepting
		// "any loopback peer" let any of them POST unauthenticated to
		// /api/v1/internal/* — flooding admin notifications to bury real
		// alerts, or forging malware/quarantine rows pointing at arbitrary
		// paths. The unix socket is the production transport (ADR-0050), so
		// refuse by default and let a legacy [host]:port deployment opt in
		// knowingly.
		if !allowTCPInternalRoutes() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "localhost_only",
				"detail": "internal routes require the unix socket (peer credentials); " +
					"this panel is bound to TCP, where any local process could call them. " +
					"Bind server.addr to unix:/run/jabali-panel/api.sock, or set " +
					"JABALI_ALLOW_TCP_INTERNAL=1 to accept the risk.",
			})
			return
		}
		host, _, err := net.SplitHostPort(remote)
		if err != nil {
			// Fallback: some adapters supply a bare host with no port.
			host = remote
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "localhost_only"})
			return
		}
		c.Next()
	}
}
