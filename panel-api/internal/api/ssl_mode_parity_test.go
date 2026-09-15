package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// TestDisableSSL_ProtectedDomainRefusal and TestEnableSSL_OperatorLineageNoop
// guard the JAB-356 HTTP-side TLS-mode invariants, closing AC2 for the legacy
// enable/disable doors (POST/DELETE /domains/:id/ssl) so HTTP matches the CLI
// door and the set-mode door.
//
// The security property under test is ORDERING: the refusal / no-op must land
// BEFORE any authoritative mutation. MockDomainRepository.UpdateSSLMode is an
// unspied stub (returns nil) shared across the api tests, so it can't be
// asserted directly; instead these tests pin the DOWNSTREAM spied side effects
// — Reconciler.Schedule, SSLCerts.UpdateStatus/MarkRevoked/Create — as NOT
// called on the guarded paths. A refusal/no-op that reached the write would
// necessarily have scheduled a reconcile or touched the cert row, so the
// negative assertions fail the moment the guard is removed (see the two
// falsification runs recorded on the PR).

func newParityHandler(dom *models.Domain, settings *models.ServerSettings, cert *models.SSLCertificate) (*sslHandler, *MockDomainRepository, *MockSSLCertificateRepository, *retryTestScheduler) {
	mockDomains := new(MockDomainRepository)
	mockDomains.On("FindByID", mock.Anything, dom.ID).Return(dom, nil)

	mockCerts := new(MockSSLCertificateRepository)
	if cert != nil {
		mockCerts.On("FindByDomainID", mock.Anything, dom.ID).Return(cert, nil)
	}
	sched := &retryTestScheduler{}

	cfg := SSLHandlerConfig{Domains: mockDomains, SSLCerts: mockCerts, Reconciler: sched}
	if settings != nil {
		cfg.ServerSettings = &mockServerSettingsRepo{getResult: settings}
	}
	return newSSLHandler(cfg), mockDomains, mockCerts, sched
}

func callParity(h *sslHandler, method, id string, fn func(*sslHandler, *gin.Context)) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request = httptest.NewRequest(method, "/api/v1/domains/"+id+"/ssl", nil)
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin", IsAdmin: true})
	fn(h, c)
	return c, w
}

func TestDisableSSL_ProtectedDomainRefusal(t *testing.T) {
	t.Run("panel-primary refused, nothing mutated", func(t *testing.T) {
		dom := &models.Domain{ID: "domain-1", Name: "panel.example.com", UserID: "u1", SSLMode: models.SSLModeLE, IsPanelPrimary: true}
		h, _, mockCerts, sched := newParityHandler(dom, nil, nil)

		c, w := callParity(h, "DELETE", "domain-1", (*sslHandler).disableSSL)

		require.Equal(t, http.StatusUnprocessableEntity, c.Writer.Status())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "ssl_none_panel_primary", body["error"])
		require.Empty(t, sched.scheduled, "refusal must not schedule a reconcile")
		mockCerts.AssertNotCalled(t, "UpdateStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		mockCerts.AssertNotCalled(t, "MarkRevoked", mock.Anything, mock.Anything)
	})

	t.Run("mail-enabled refused, nothing mutated", func(t *testing.T) {
		dom := &models.Domain{ID: "domain-1", Name: "mail.example.com", UserID: "u1", SSLMode: models.SSLModeLE, EmailEnabled: true}
		h, _, mockCerts, sched := newParityHandler(dom, nil, nil)

		c, w := callParity(h, "DELETE", "domain-1", (*sslHandler).disableSSL)

		require.Equal(t, http.StatusUnprocessableEntity, c.Writer.Status())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "ssl_none_with_email", body["error"])
		require.Empty(t, sched.scheduled, "refusal must not schedule a reconcile")
		mockCerts.AssertNotCalled(t, "UpdateStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		mockCerts.AssertNotCalled(t, "MarkRevoked", mock.Anything, mock.Anything)
	})

	t.Run("ordinary domain proceeds and schedules", func(t *testing.T) {
		dom := &models.Domain{ID: "domain-1", Name: "site.example.com", UserID: "u1", SSLMode: models.SSLModeLE}
		// No cert row (FindByDomainID returns not-found) so the revoke-mark is skipped.
		h, _, mockCerts, sched := newParityHandler(dom, nil, nil)
		mockCerts.On("FindByDomainID", mock.Anything, "domain-1").Return(nil, repository.ErrNotFound)

		c, _ := callParity(h, "DELETE", "domain-1", (*sslHandler).disableSSL)

		require.Equal(t, http.StatusAccepted, c.Writer.Status())
		require.Equal(t, []string{"domain-1"}, sched.scheduled, "an allowed disable must schedule the reconcile")
	})
}

func TestEnableSSL_OperatorLineageNoop(t *testing.T) {
	for _, mode := range []string{models.SSLModeCustom, models.SSLModeShared} {
		t.Run("enable is a no-op on "+mode, func(t *testing.T) {
			dom := &models.Domain{ID: "domain-1", Name: "op.example.com", UserID: "u1", SSLMode: mode}
			// ServerSettings/Config are nil: the no-op must return before the
			// admin_email gate and before any cert work.
			h, _, mockCerts, sched := newParityHandler(dom, nil, nil)

			c, w := callParity(h, "POST", "domain-1", (*sslHandler).enableSSL)

			require.Equal(t, http.StatusOK, c.Writer.Status())
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, mode, body["ssl_mode"])
			require.Empty(t, sched.scheduled, "no-op must not schedule a reconcile")
			mockCerts.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
			mockCerts.AssertNotCalled(t, "FindByDomainID", mock.Anything, mock.Anything)
		})
	}

	t.Run("enable from none still drives le", func(t *testing.T) {
		dom := &models.Domain{ID: "domain-1", Name: "site.example.com", UserID: "u1", SSLMode: models.SSLModeNone}
		settings := &models.ServerSettings{AdminEmail: "ops@example.com"}
		// Existing (non-issued) cert row → the else-branch marks it pending, so
		// the enable path never touches Config.ACME (Create branch only).
		cert := &models.SSLCertificate{ID: "cert-1", DomainID: "domain-1", Status: models.SSLStatusPending}
		h, _, mockCerts, sched := newParityHandler(dom, settings, cert)
		mockCerts.On("UpdateStatus", mock.Anything, "cert-1", models.SSLStatusPending, mock.Anything).Return(nil)

		c, _ := callParity(h, "POST", "domain-1", (*sslHandler).enableSSL)

		require.Equal(t, http.StatusAccepted, c.Writer.Status())
		require.Equal(t, []string{"domain-1"}, sched.scheduled, "a real enable must schedule the reconcile")
		mockCerts.AssertCalled(t, "UpdateStatus", mock.Anything, "cert-1", models.SSLStatusPending, mock.Anything)
	})
}
