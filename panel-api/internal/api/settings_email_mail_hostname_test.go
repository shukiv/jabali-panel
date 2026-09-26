package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-390 setter door. PUT records a switchover request (the reconciler
// applies it once the certificate is issued), DELETE withdraws one that is
// not in flight, and GET shows the request's progress. Every PUT and DELETE
// is audited, refused or not.

type mhSettings struct {
	repository.ServerSettingsRepository
	s *models.ServerSettings
}

func (f mhSettings) Get(context.Context) (*models.ServerSettings, error) {
	cp := *f.s
	return &cp, nil
}

type mhCerts struct {
	repository.PanelCertificateRepository
	useLE bool
}

func (f *mhCerts) GetByKind(_ context.Context, kind string) (*models.PanelCertificate, error) {
	return &models.PanelCertificate{Kind: kind, UseLE: f.useLE}, nil
}

type mhSwitchover struct {
	repository.MailHostnameSwitchoverRepository
	row       *models.MailHostnameSwitchover
	requested []string
	cancelErr error
	cancels   int
}

func (f *mhSwitchover) Get(context.Context) (*models.MailHostnameSwitchover, error) {
	if f.row == nil {
		return nil, repository.ErrNotFound
	}
	return f.row, nil
}

func (f *mhSwitchover) Request(_ context.Context, desired, _ string, now time.Time) error {
	f.requested = append(f.requested, desired)
	f.row = &models.MailHostnameSwitchover{ID: 1, Desired: &desired, Status: models.MailHostnameSwitchoverPending, UpdatedAt: now}
	return nil
}

func (f *mhSwitchover) Cancel(context.Context, time.Time) error {
	f.cancels++
	return f.cancelErr
}

type mhRecorder struct{ events []*models.AuditEvent }

func (r *mhRecorder) Record(e *models.AuditEvent) { r.events = append(r.events, e) }

type mhFixture struct {
	router *gin.Engine
	sw     *mhSwitchover
	rec    *mhRecorder
	certs  *mhCerts
}

func newMailHostnameFixture(t *testing.T) *mhFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := newMockDomainRepo()
	repo.domains["dom_panel"] = &models.Domain{ID: "dom_panel", Name: "panel.example.com", IsPanelPrimary: true, EmailEnabled: true, WebmailEnabled: true}
	repo.domains["dom_tenant"] = &models.Domain{ID: "dom_tenant", Name: "tenant.net", UserID: "u2"}
	f := &mhFixture{sw: &mhSwitchover{}, rec: &mhRecorder{}, certs: &mhCerts{useLE: true}}
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
		c.Next()
	})
	RegisterSettingsEmailRoutes(v1, SettingsEmailHandlerConfig{
		Domains: repo,
		ServerSettings: mhSettings{s: &models.ServerSettings{Hostname: "panel.example.com", AdminEmail: "admin@example.com",
			WebmailEnabled: true}},
		PanelCerts: f.certs,
		Switchover: f.sw,
		Recorder:   f.rec,
		Log:        slog.Default(),
	})
	f.router = r
	return f
}

func (f *mhFixture) do(t *testing.T, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/v1/admin/settings/email/mail-hostname", nil)
	} else {
		req = httptest.NewRequest(method, "/api/v1/admin/settings/email/mail-hostname", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestSettingsEmailMailHostname_PutRecordsRequest(t *testing.T) {
	f := newMailHostnameFixture(t)
	rec := f.do(t, http.MethodPut, `{"mail_hostname":" MX.Example.ORG "}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"mx.example.org"}, f.sw.requested)

	var body struct {
		Switchover struct {
			Desired string `json:"desired"`
			Status  string `json:"status"`
		} `json:"switchover"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "mx.example.org", body.Switchover.Desired)
	assert.Equal(t, "pending", body.Switchover.Status)

	require.Len(t, f.rec.events, 1)
	assert.Equal(t, "settings.mail_hostname.request", f.rec.events[0].Action)
	assert.Equal(t, "mx.example.org", f.rec.events[0].TargetID)
	assert.Equal(t, models.AuditResultOK, f.rec.events[0].Result)
}

func TestSettingsEmailMailHostname_PutRefusals(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		mutate func(*mhFixture)
		status int
		code   string
	}{
		{"invalid", `{"mail_hostname":"https://mx.example.org/"}`, nil, http.StatusBadRequest, "invalid_mail_hostname"},
		{"not ready", `{"mail_hostname":"mx.example.org"}`, func(f *mhFixture) { f.certs.useLE = false }, http.StatusConflict, "mail_hostname_not_ready"},
		{"unchanged", `{"mail_hostname":"mail.panel.example.com"}`, nil, http.StatusConflict, "mail_hostname_refused"},
		{"tenant zone", `{"mail_hostname":"mx.tenant.net"}`, nil, http.StatusConflict, "mail_hostname_refused"},
		{"malformed body", `{"mail_hostname":`, nil, http.StatusBadRequest, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMailHostnameFixture(t)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			rec := f.do(t, http.MethodPut, tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"error":"`+tc.code+`"`)
			assert.Empty(t, f.sw.requested, "a refused request is not recorded")
			if tc.code != "invalid_request" {
				require.Len(t, f.rec.events, 1, "a refusal is audited")
				assert.Equal(t, models.AuditResultDenied, f.rec.events[0].Result)
			}
		})
	}
}

func TestSettingsEmailMailHostname_Delete(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		result string
	}{
		{"cancelled", nil, http.StatusOK, models.AuditResultOK},
		{"nothing to cancel", repository.ErrNotFound, http.StatusNotFound, models.AuditResultDenied},
		{"in flight", repository.ErrSwitchoverInFlight, http.StatusConflict, models.AuditResultDenied},
		{"store error", errors.New("db down"), http.StatusInternalServerError, models.AuditResultError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMailHostnameFixture(t)
			f.sw.cancelErr = tc.err
			rec := f.do(t, http.MethodDelete, "")
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, 1, f.sw.cancels)
			require.Len(t, f.rec.events, 1)
			assert.Equal(t, "settings.mail_hostname.cancel", f.rec.events[0].Action)
			assert.Equal(t, tc.result, f.rec.events[0].Result)
		})
	}
}

func TestSettingsEmail_GetShowsSwitchover(t *testing.T) {
	f := newMailHostnameFixture(t)
	get := func() map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/email", nil)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body
	}

	body := get()
	require.Contains(t, body, "switchover")
	assert.Nil(t, body["switchover"], "no request: switchover is null")

	desired := "mx.example.org"
	retry := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	f.sw.row = &models.MailHostnameSwitchover{ID: 1, Desired: &desired, Status: models.MailHostnameSwitchoverFailed,
		LastError: "mx.example.org does not point at this server (dns lookup failed)", NextRetryAt: &retry,
		UpdatedAt: time.Date(2026, 9, 27, 10, 50, 0, 0, time.UTC)}
	sw, ok := get()["switchover"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "mx.example.org", sw["desired"])
	assert.Equal(t, "failed", sw["status"])
	assert.Equal(t, "mx.example.org does not point at this server (dns lookup failed)", sw["last_error"])
	assert.Equal(t, "2026-09-27T11:00:00Z", sw["next_retry_at"])

	f.sw.row = &models.MailHostnameSwitchover{ID: 1, Status: models.MailHostnameSwitchoverIdle}
	assert.Nil(t, get()["switchover"], "a cancelled request is shown as none")
}
