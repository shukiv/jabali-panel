package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ssl_ownership_test.go — GH #2200. A domain whose owner has not proven the
// name yet (GH #1816) gets a self-signed placeholder: the reconciler asks no
// CA for it. Renew and Retry changed nothing there (the next pass self-signs
// it again), and the SSL Manager read "Self SSL mode" or "while Let's Encrypt
// issuance retries", neither of which was true.

func TestRenewRetrySSL_RefuseOwnershipPending(t *testing.T) {
	for name, fn := range map[string]func(*sslHandler, *gin.Context){
		"renew": (*sslHandler).renewSSL,
		"retry": (*sslHandler).retrySSL,
	} {
		t.Run(name, func(t *testing.T) {
			dom := &models.Domain{ID: "domain-1", Name: "site.example.com", UserID: "u1", SSLMode: models.SSLModeLE}
			dom.OwnershipStatus = models.OwnershipPending
			cert := &models.SSLCertificate{ID: "cert-1", DomainID: "domain-1", Status: models.SSLStatusPendingACMERetry}
			h, _, mockCerts, sched := newParityHandler(dom, nil, cert)
			// Registered so a door that doesn't refuse reaches its write and
			// fails the assertions below, rather than panicking in the mock.
			mockCerts.On("UpdateStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
			mockCerts.On("ResetForRetry", mock.Anything, mock.Anything, mock.Anything).Return(nil)

			c, w := callParity(h, "POST", "domain-1", fn)

			require.Equal(t, http.StatusConflict, c.Writer.Status())
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, "domain_ownership_pending", body["error"])
			require.Contains(t, body["detail"], "proof of ownership")
			require.Empty(t, sched.scheduled, "a refusal must not schedule a reconcile")
			mockCerts.AssertNotCalled(t, "UpdateStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			mockCerts.AssertNotCalled(t, "ResetForRetry", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestEnrichCertRows_OwnershipPending(t *testing.T) {
	h := newSSLHandler(SSLHandlerConfig{SSLCerts: new(MockSSLCertificateRepository)})
	rows := h.enrichCertRows(context.Background(), []repository.SSLCertificateWithDomain{
		{ID: "c1", DomainName: "pending.example.com", Status: models.SSLStatusSelfSigned, SSLMode: models.SSLModeLE, OwnershipStatus: models.OwnershipPending},
		{ID: "c2", DomainName: "proven.example.com", Status: models.SSLStatusIssued, SSLMode: models.SSLModeLE, OwnershipStatus: models.OwnershipVerified},
		{ID: "c3", DomainName: "custom.example.com", Status: models.SSLStatusCustom, SSLMode: models.SSLModeCustom, OwnershipStatus: models.OwnershipPending},
		// The mail rows carry no ownership status; they never read as pending.
		{ID: "mail-cert:d4", DomainName: "mail.example.com", Status: models.SSLStatusIssued},
	})
	require.Len(t, rows, 4)
	require.True(t, rows[0].OwnershipPending, "an unproven Let's Encrypt domain waits for proof")
	require.False(t, rows[1].OwnershipPending)
	require.False(t, rows[2].OwnershipPending, "a custom certificate is the owner's own")
	require.False(t, rows[3].OwnershipPending)

	raw, err := json.Marshal(rows[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"ownership_pending":true`)
	raw, err = json.Marshal(rows[1])
	require.NoError(t, err)
	require.NotContains(t, string(raw), "ownership_pending")
}
