package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"github.com/gin-gonic/gin"
)

// JAB-318 AC4: the PATCH /domains/:id handler must apply its writes — the
// general Update plus every dedicated writer (listen_ips / mail_provider /
// ssl_mode) — through Domains.Transaction, so a mid-sequence failure rolls the
// whole apply back (the repository sqlmock test proves the rollback) and a
// partial write is never reported as success. These handler tests pin the
// wiring: the apply routes through Transaction, and a writer failure surfaces
// as 500.

func applyTxHarness(t *testing.T, dom *models.Domain) (*gin.Engine, *mockDomainRepo) {
	t.Helper()
	ips := &fakeManagedIPsForDomain{rows: []models.ManagedIP{
		{ID: 1, Address: "203.0.113.1", Family: "ipv4", IsDefault: true},
	}}
	return setupDomainListenIPHarness(t,
		&auth.AccessClaims{UserID: dom.UserID, IsAdmin: true}, ips, dom)
}

func patchDomainBody(r *gin.Engine, id, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/domains/"+id, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// TestDomainPatch_Apply_RoutesWritesThroughTransaction: the wiring guarantee —
// the handler applies its writes THROUGH Domains.Transaction (so they inherit
// the repository's all-or-nothing rollback) rather than issuing them directly.
// Falsify by unwrapping the handler's transaction: the writes run outside
// Transaction, txCalls stays 0, and this reddens.
func TestDomainPatch_Apply_RoutesWritesThroughTransaction(t *testing.T) {
	dom := &models.Domain{ID: "d1", UserID: "u1", Name: "example.com", MailProvider: models.MailProviderJabali}
	r, repo := applyTxHarness(t, dom)

	w := patchDomainBody(r, "d1", `{"index_priority":"php_first"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", w.Code, w.Body.String())
	}
	if repo.txCalls != 1 {
		t.Fatalf("txCalls = %d, want 1 — PATCH apply must route its writes through Domains.Transaction (JAB-318 AC4)", repo.txCalls)
	}
}

// TestDomainPatch_Apply_WriterFailureReturns500: a dedicated writer failing
// inside the apply transaction surfaces as 500 — the partial write is rolled
// back (repository test) and the operation reports failure, never a partial
// success (AC4).
func TestDomainPatch_Apply_WriterFailureReturns500(t *testing.T) {
	dom := &models.Domain{ID: "d1", UserID: "u1", Name: "example.com", MailProvider: models.MailProviderJabali}
	r, repo := applyTxHarness(t, dom)
	repo.sslModeErr = errors.New("boom: ssl writer failed")

	w := patchDomainBody(r, "d1", `{"ssl_mode":"le"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500: %s", w.Code, w.Body.String())
	}
	if repo.txCalls != 1 {
		t.Fatalf("txCalls = %d, want 1 (the apply still routed through Transaction)", repo.txCalls)
	}
}
