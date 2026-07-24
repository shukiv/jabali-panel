package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func apiGenCert(t *testing.T, dns []string, cn string) (certPEM, keyPEM string) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dns,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return
}

type fakeSharedAgent struct {
	installResp string
	deleteCalls []string
}

func (a *fakeSharedAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	switch cmd {
	case "ssl.install_shared":
		return json.RawMessage(a.installResp), nil
	case "ssl.delete_shared":
		if m, ok := params.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				a.deleteCalls = append(a.deleteCalls, id)
			}
		}
	}
	return json.RawMessage(`{"ok":true}`), nil
}

type fakeSharedRepo struct {
	created  []*models.SharedCertificate
	byID     map[string]*models.SharedCertificate
	attached map[string]int64
	deleted  []string
}

func (r *fakeSharedRepo) Create(_ context.Context, c *models.SharedCertificate) error {
	r.created = append(r.created, c)
	if r.byID == nil {
		r.byID = map[string]*models.SharedCertificate{}
	}
	r.byID[c.ID] = c
	return nil
}
func (r *fakeSharedRepo) FindByID(_ context.Context, id string) (*models.SharedCertificate, error) {
	if c, ok := r.byID[id]; ok {
		return c, nil
	}
	return nil, repository.ErrNotFound
}
func (r *fakeSharedRepo) ListAll(_ context.Context) ([]models.SharedCertificate, error) {
	out := make([]models.SharedCertificate, 0, len(r.created))
	for _, c := range r.created {
		out = append(out, *c)
	}
	return out, nil
}
func (r *fakeSharedRepo) ListByUserID(_ context.Context, _ string) ([]models.SharedCertificate, error) {
	return nil, nil
}
func (r *fakeSharedRepo) ListServerWideAndOwned(_ context.Context, _ string) ([]models.SharedCertificate, error) {
	out := make([]models.SharedCertificate, 0, len(r.created))
	for _, c := range r.created {
		out = append(out, *c)
	}
	return out, nil
}
func (r *fakeSharedRepo) UpdateInstalled(_ context.Context, _, _, _, _ string, _ time.Time) error {
	return nil
}
func (r *fakeSharedRepo) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	delete(r.byID, id)
	return nil
}
func (r *fakeSharedRepo) CountAttachedDomains(_ context.Context, id string) (int64, error) {
	return r.attached[id], nil
}

// JAB-170 phase 4: upload validates + installs, stores the parsed SANs + expiry.
func TestUploadSharedCert(t *testing.T) {
	certPEM, keyPEM := apiGenCert(t, []string{"*.example.com"}, "example.com")
	ag := &fakeSharedAgent{installResp: `{"cert_path":"/etc/jabali/ssl/shared/X/fullchain.pem","key_path":"/etc/jabali/ssl/shared/X/privkey.pem","sans":["*.example.com","example.com"],"expires_at":"2027-01-01T00:00:00Z"}`}
	repo := &fakeSharedRepo{}
	h := newSSLHandler(SSLHandlerConfig{Agent: ag, SharedCerts: repo})

	body, _ := json.Marshal(map[string]string{"name": "wild", "cert_pem": certPEM, "key_pem": keyPEM})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/v1/admin/certificates/shared", bytes.NewReader(body))
	h.uploadSharedCert(c)

	require.Equal(t, http.StatusCreated, w.Code)
	require.Len(t, repo.created, 1)
	require.Equal(t, "wild", repo.created[0].Name)
	require.Nil(t, repo.created[0].UserID) // server-wide
	require.NotNil(t, repo.created[0].SANs)
	require.Contains(t, *repo.created[0].SANs, "example.com")
	require.NotNil(t, repo.created[0].ExpiresAt)
}

// Delete refuses while domains are attached (they'd lose their cert).
func TestDeleteSharedCert_AttachedBlocks(t *testing.T) {
	repo := &fakeSharedRepo{byID: map[string]*models.SharedCertificate{"C1": {ID: "C1"}}, attached: map[string]int64{"C1": 2}}
	ag := &fakeSharedAgent{}
	h := newSSLHandler(SSLHandlerConfig{Agent: ag, SharedCerts: repo})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "C1"}}
	c.Request = httptest.NewRequest("DELETE", "/api/v1/admin/certificates/shared/C1", nil)
	h.deleteSharedCert(c)

	require.Equal(t, http.StatusConflict, w.Code)
	require.Empty(t, repo.deleted)
	require.Empty(t, ag.deleteCalls)
}

// Unattached delete removes the row + the on-disk pair.
func TestDeleteSharedCert_OK(t *testing.T) {
	repo := &fakeSharedRepo{byID: map[string]*models.SharedCertificate{"C1": {ID: "C1"}}, attached: map[string]int64{"C1": 0}}
	ag := &fakeSharedAgent{}
	h := newSSLHandler(SSLHandlerConfig{Agent: ag, SharedCerts: repo})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "C1"}}
	c.Request = httptest.NewRequest("DELETE", "/api/v1/admin/certificates/shared/C1", nil)
	h.deleteSharedCert(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{"C1"}, repo.deleted)
	require.Equal(t, []string{"C1"}, ag.deleteCalls)
}

// JAB-170 phase 4b: wildcard-aware SAN matching (x509.VerifyHostname semantics).
// A bug here would serve the wrong cert, so it's exhaustively tabled.
func TestHostMatchesSAN(t *testing.T) {
	for _, c := range []struct {
		san, host string
		want      bool
	}{
		{"example.com", "example.com", true},
		{"Example.com", "example.COM", true},        // case-insensitive
		{"*.example.com", "sub.example.com", true},  // single-label wildcard
		{"*.example.com", "example.com", false},     // wildcard does NOT cover the apex
		{"*.example.com", "a.b.example.com", false}, // wildcard is one label only
		{"*.example.com", "sub.other.com", false},   // different base
		{"example.com", "sub.example.com", false},   // exact SAN doesn't cover subdomain
		{"", "example.com", false},
		{"*.example.com", ".example.com", false}, // empty leftmost label
	} {
		if got := hostMatchesSAN(c.san, c.host); got != c.want {
			t.Errorf("hostMatchesSAN(%q,%q)=%v want %v", c.san, c.host, got, c.want)
		}
	}
}

func TestSharedCertCoversHost(t *testing.T) {
	sans := `["*.example.com","example.com"]`
	require.True(t, sharedCertCoversHost(&sans, "sub.example.com"))
	require.True(t, sharedCertCoversHost(&sans, "example.com"))
	require.False(t, sharedCertCoversHost(&sans, "other.com"))
	require.False(t, sharedCertCoversHost(nil, "x.com"))
	bad := `not json`
	require.False(t, sharedCertCoversHost(&bad, "example.com"))
}
