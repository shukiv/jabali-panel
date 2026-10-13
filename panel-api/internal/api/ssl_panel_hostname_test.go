package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ssl_panel_hostname_test.go — GH #2126. The panel hostname's certificate is
// the panel's own (Server Settings → Panel SSL). The per-domain doors created
// a certificate row for it that the reconciler could only fail on — the row
// has no document root — so they refuse it before touching anything.

func TestPerDomainSSLDoors_RefusePanelHostname(t *testing.T) {
	doors := []struct {
		name string
		fn   func(*sslHandler, *gin.Context)
	}{
		{"enable", (*sslHandler).enableSSL},
		{"renew", (*sslHandler).renewSSL},
		{"retry", (*sslHandler).retrySSL},
	}
	for _, d := range doors {
		t.Run(d.name, func(t *testing.T) {
			dom := &models.Domain{ID: "domain-1", Name: "panel.example.com", UserID: "admin", SSLMode: models.SSLModeLE, IsPanelPrimary: true}
			cert := &models.SSLCertificate{ID: "cert-1", DomainID: "domain-1", Status: models.SSLStatusFailed}
			h, _, mockCerts, sched := newParityHandler(dom, &models.ServerSettings{AdminEmail: "ops@example.com"}, cert)
			// Registered so a door that doesn't refuse reaches its write and
			// fails the assertions below, rather than panicking in the mock.
			mockCerts.On("Create", mock.Anything, mock.Anything).Return(nil)
			mockCerts.On("UpdateStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
			mockCerts.On("ResetForRetry", mock.Anything, mock.Anything, mock.Anything).Return(nil)

			c, w := callParity(h, "POST", "domain-1", d.fn)

			require.Equal(t, http.StatusConflict, c.Writer.Status())
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, "panel_hostname_certificate", body["error"])
			require.Contains(t, body["detail"], "Panel SSL")
			require.Empty(t, sched.scheduled, "a refusal must not schedule a reconcile")
			mockCerts.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
			mockCerts.AssertNotCalled(t, "UpdateStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			mockCerts.AssertNotCalled(t, "ResetForRetry", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// renew and retry now load the domain; an unknown one is a 404, before any
// certificate lookup.
func TestRenewRetrySSL_UnknownDomain404(t *testing.T) {
	for name, fn := range map[string]func(*sslHandler, *gin.Context){
		"renew": (*sslHandler).renewSSL,
		"retry": (*sslHandler).retrySSL,
	} {
		t.Run(name, func(t *testing.T) {
			dom := &models.Domain{ID: "domain-1", Name: "site.example.com", UserID: "u1"}
			h, mockDomains, mockCerts, sched := newParityHandler(dom, nil, nil)
			mockDomains.On("FindByID", mock.Anything, "missing").Return(nil, repository.ErrNotFound)
			mockCerts.On("FindByDomainID", mock.Anything, "missing").Return(nil, repository.ErrNotFound)

			c, w := callParity(h, "POST", "missing", fn)

			require.Equal(t, http.StatusNotFound, c.Writer.Status())
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, "not_found", body["error"], "the domain lookup answers, not the certificate lookup")
			require.Empty(t, sched.scheduled)
		})
	}
}
