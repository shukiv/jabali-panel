package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", RequireLocalhost(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func requestWithRemote(t *testing.T, r *gin.Engine, remote string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// A loopback TCP peer is NO LONGER trusted by default.
//
// This reverses the middleware's original contract deliberately. On a
// multi-tenant box loopback is not a trust boundary: every tenant process (a
// PHP-FPM pool, a cron job, an SSH shell) can connect to 127.0.0.1, and the
// routes behind this gate have no other authentication — an unauthenticated
// local caller could flood admin notifications to bury real alerts, or forge
// malware/quarantine rows pointing at arbitrary paths. Production binds the
// unix socket (ADR-0050), where SO_PEERCRED gives a real identity.
func TestRequireLocalhost_RejectsLoopbackTCPByDefault(t *testing.T) {
	t.Setenv("JABALI_ALLOW_TCP_INTERNAL", "")
	for _, remote := range []string{"127.0.0.1:54321", "[::1]:54321"} {
		if code := requestWithRemote(t, setupRouter(), remote); code != http.StatusForbidden {
			t.Errorf("remote %s: want 403 (loopback TCP is not a trust boundary), got %d", remote, code)
		}
	}
}

// The opt-in restores the legacy behaviour for a deployment that still binds
// [host]:port and accepts the risk knowingly.
func TestRequireLocalhost_LoopbackTCPAllowedWithOptIn(t *testing.T) {
	t.Setenv("JABALI_ALLOW_TCP_INTERNAL", "1")
	for _, remote := range []string{"127.0.0.1:54321", "[::1]:54321"} {
		if code := requestWithRemote(t, setupRouter(), remote); code != http.StatusOK {
			t.Errorf("remote %s: want 200 with JABALI_ALLOW_TCP_INTERNAL=1, got %d", remote, code)
		}
	}
	// A NON-loopback peer stays rejected even with the opt-in — the escape
	// hatch relaxes the transport requirement, not the address check.
	if code := requestWithRemote(t, setupRouter(), "8.8.8.8:54321"); code != http.StatusForbidden {
		t.Errorf("public IP must stay rejected even with the opt-in, got %d", code)
	}
}

func TestRequireLocalhost_UnixSocketAtSentinel(t *testing.T) {
	// net/http sets RemoteAddr="@" for unix-socket peers.
	if code := requestWithRemote(t, setupRouter(), "@"); code != http.StatusOK {
		t.Fatalf("want 200 for unix-socket peer, got %d", code)
	}
}

func TestRequireLocalhost_UnixSocketEmpty(t *testing.T) {
	// Some adapters set RemoteAddr="" instead of "@" for unix-socket peers.
	if code := requestWithRemote(t, setupRouter(), ""); code != http.StatusOK {
		t.Fatalf("want 200 for empty-RemoteAddr unix-socket peer, got %d", code)
	}
}

func TestRequireLocalhost_RejectsPublicIPv4(t *testing.T) {
	if code := requestWithRemote(t, setupRouter(), "8.8.8.8:54321"); code != http.StatusForbidden {
		t.Fatalf("want 403 for public IP, got %d", code)
	}
}

func TestRequireLocalhost_RejectsPrivateIPv4(t *testing.T) {
	if code := requestWithRemote(t, setupRouter(), "10.0.0.5:54321"); code != http.StatusForbidden {
		t.Fatalf("want 403 for non-loopback private IP, got %d", code)
	}
}
