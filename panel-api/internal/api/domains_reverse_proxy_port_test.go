package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// proxyPortDomains serves FindByID and records SetReverseProxyPort.
type proxyPortDomains struct {
	repository.DomainRepository
	byID   *models.Domain
	setErr error
	sets   []int
}

func (r *proxyPortDomains) FindByID(context.Context, string) (*models.Domain, error) {
	if r.byID == nil {
		return nil, repository.ErrNotFound
	}
	d := *r.byID
	return &d, nil
}

func (r *proxyPortDomains) SetReverseProxyPort(_ context.Context, _ string, port int) error {
	r.sets = append(r.sets, port)
	return r.setErr
}

func proxyPortCtx(claims *auth.AccessClaims, body string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/domains/d1/reverse-proxy-port", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "d1"}}
	if claims != nil {
		ginctx.SetClaims(c, claims)
	}
	return c, w
}

func proxyPortHandler(repo *proxyPortDomains) *domainHandler {
	return &domainHandler{cfg: DomainHandlerConfig{Domains: repo}}
}

func proxyDomainRow() *models.Domain {
	return &models.Domain{ID: "d1", UserID: "u1", Name: "app.example", ReverseProxyPort: 3000}
}

// GH #2544: the domain's own tenant moves its reverse proxy to another port.
func TestSetReverseProxyPort_OwnerChangesPort(t *testing.T) {
	repo := &proxyPortDomains{byID: proxyDomainRow()}
	c, w := proxyPortCtx(&auth.AccessClaims{UserID: "u1"}, `{"port":5000}`)
	proxyPortHandler(repo).setReverseProxyPort(c)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []int{5000}, repo.sets)
	assert.Contains(t, w.Body.String(), `"reverse_proxy_port":5000`)
}

func TestSetReverseProxyPort_AdminChangesAnyDomain(t *testing.T) {
	repo := &proxyPortDomains{byID: proxyDomainRow()}
	c, w := proxyPortCtx(&auth.AccessClaims{UserID: "admin1", IsAdmin: true}, `{"port":5000}`)
	proxyPortHandler(repo).setReverseProxyPort(c)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []int{5000}, repo.sets)
}

func TestSetReverseProxyPort_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claims *auth.AccessClaims
		dom    *models.Domain
		setErr error
		body   string
		status int
		code   string
	}{
		{"unauthenticated", nil, proxyDomainRow(), nil, `{"port":5000}`, http.StatusUnauthorized, "unauthenticated"},
		{"another tenant's domain", &auth.AccessClaims{UserID: "u2"}, proxyDomainRow(), nil, `{"port":5000}`, http.StatusForbidden, "forbidden"},
		{"no such domain", &auth.AccessClaims{UserID: "u1"}, nil, nil, `{"port":5000}`, http.StatusNotFound, "not_found"},
		{"no port", &auth.AccessClaims{UserID: "u1"}, proxyDomainRow(), nil, `{}`, http.StatusBadRequest, "invalid_request"},
		{"not json", &auth.AccessClaims{UserID: "u1"}, proxyDomainRow(), nil, `port=5000`, http.StatusBadRequest, "invalid_request"},
		{"a docroot domain", &auth.AccessClaims{UserID: "u1"}, &models.Domain{ID: "d1", UserID: "u1"}, nil, `{"port":5000}`, http.StatusConflict, "not_reverse_proxy"},
		{"a forbidden port", &auth.AccessClaims{UserID: "u1"}, proxyDomainRow(), nil, `{"port":8443}`, http.StatusBadRequest, "reverse_proxy_port_invalid"},
		{"a privileged port", &auth.AccessClaims{UserID: "u1"}, proxyDomainRow(), nil, `{"port":80}`, http.StatusBadRequest, "reverse_proxy_port_invalid"},
		{"another domain's port", &auth.AccessClaims{UserID: "u1"}, proxyDomainRow(), repository.ErrPortInUse, `{"port":5000}`, http.StatusConflict, "reverse_proxy_port_in_use"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &proxyPortDomains{byID: tc.dom, setErr: tc.setErr}
			c, w := proxyPortCtx(tc.claims, tc.body)
			proxyPortHandler(repo).setReverseProxyPort(c)
			assert.Equal(t, tc.status, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), tc.code)
			if tc.setErr == nil {
				assert.Empty(t, repo.sets, "nothing may be written on a refusal")
			}
		})
	}
}
