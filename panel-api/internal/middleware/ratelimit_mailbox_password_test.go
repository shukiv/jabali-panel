package middleware_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

// The mailbox password page (GH #2543) spends a per-address budget and, when
// the form names a mailbox, that mailbox's budget, twice an address's.

// mailboxPasswordLimiter allows addrBurst tries per address and twice
// strictBurst per mailbox, no refill.
func mailboxPasswordLimiter(addrBurst, strictBurst int) *middleware.RateLimiter {
	return middleware.NewRateLimiter(middleware.RateLimiterConfig{
		DefaultRate: 1000, DefaultBurst: 1000,
		KratosRate: 0, KratosBurst: addrBurst,
		StrictRate: 0, StrictBurst: strictBurst,
	})
}

func tryMailboxPassword(l *middleware.RateLimiter, ip, email string, log *slog.Logger) bool {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/mailbox-password", nil)
	req.RemoteAddr = ip + ":54321"
	c.Request = req
	return l.AllowMailboxPassword(c, email, log)
}

func TestAllowMailboxPassword_PerAddressBudgetAndMarker(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	l := mailboxPasswordLimiter(3, 100)
	for i := 0; i < 3; i++ {
		if !tryMailboxPassword(l, "203.0.113.7", "", log) {
			t.Fatalf("try %d refused inside the budget", i+1)
		}
	}
	if tryMailboxPassword(l, "203.0.113.7", "", log) {
		t.Fatal("a fourth try from the same address passed a budget of 3")
	}
	if !strings.Contains(buf.String(), `"marker":"mailbox_password_rate_limited"`) ||
		!strings.Contains(buf.String(), `"client_ip":"203.0.113.7"`) {
		t.Fatalf("the refusal must log the CrowdSec marker with client_ip; got %s", buf.String())
	}
	if !tryMailboxPassword(l, "198.51.100.9", "", log) {
		t.Fatal("another address must have its own budget")
	}
}

// Its own bucket: panel sign-ins from the address don't spend this page's
// budget, nor the other way round.
func TestAllowMailboxPassword_OwnBucketApartFromSignIn(t *testing.T) {
	l := mailboxPasswordLimiter(1, 100)
	login := kratosPost{path: kratosLogin, ip: "203.0.113.7", contentType: jsonType, body: `{}`}
	for i := 0; i < 2; i++ {
		serveKratos(l.KratosFlows(nil), login)
	}
	if !tryMailboxPassword(l, "203.0.113.7", "", nil) {
		t.Fatal("panel sign-ins spent the mailbox password page's budget")
	}
	l = mailboxPasswordLimiter(1, 100)
	tryMailboxPassword(l, "203.0.113.7", "", nil)
	tryMailboxPassword(l, "203.0.113.7", "", nil)
	if w, _ := serveKratos(l.KratosFlows(nil), login); w.Code == http.StatusTooManyRequests {
		t.Fatal("the mailbox password page spent the panel sign-in budget")
	}
}

func TestAllowMailboxPassword_MailboxBudgetAcrossAddresses(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	l := mailboxPasswordLimiter(100, 2)
	// Twice the Strict tier's budget for one mailbox, from many addresses, and
	// the address case and spaces don't make a new mailbox.
	ips := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.4"}
	names := []string{"alice@example.com", " Alice@Example.com", "ÀLICE@example.com ", "alice@EXAMPLE.com"}
	for i := range ips {
		if !tryMailboxPassword(l, ips[i], names[i], log) {
			t.Fatalf("try %d refused inside the mailbox budget of 4", i+1)
		}
	}
	if tryMailboxPassword(l, "203.0.113.5", "alice@example.com", log) {
		t.Fatal("a fifth try at one mailbox passed its budget of 4")
	}
	out := buf.String()
	if !strings.Contains(out, `"marker":"mailbox_password_account_rate_limited"`) {
		t.Fatalf("the mailbox refusal must log its own marker; got %s", out)
	}
	if strings.Contains(out, `"marker":"mailbox_password_rate_limited"`) {
		t.Fatal("a mailbox over its budget must not log the per-address marker CrowdSec bans on")
	}
	if strings.Contains(strings.ToLower(out), "alice@example.com") {
		t.Fatal("the log must carry a hash, never the address")
	}
	if !tryMailboxPassword(l, "203.0.113.5", "bob@example.com", log) {
		t.Fatal("another mailbox must have its own budget")
	}
}
