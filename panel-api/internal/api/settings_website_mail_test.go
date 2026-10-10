package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/smarthost"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// GH #2056, ADR 0174: website mail through the local mail server or the
// operator's smarthost. Pinned: switching to a smarthost tests it first and
// saves nothing when the test fails; the password lands sealed, is never
// returned, and an empty password keeps the stored one; a login over
// plaintext and non-mail ports are refused.

type probeRecorder struct {
	calls []smarthost.Config
	err   error
}

func (p *probeRecorder) probe(_ context.Context, c smarthost.Config) error {
	p.calls = append(p.calls, c)
	return p.err
}

func websiteMailRouter(t *testing.T, repo *mockServerSettingsRepo, key *ssokey.Key, p *probeRecorder, admin bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: admin})
	})
	RegisterWebsiteMailRoutes(r.Group("/api/v1"), WebsiteMailHandlerConfig{Repo: repo, SSOKey: key, Probe: p.probe})
	return r
}

func websiteMailCall(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/admin/settings/website-mail"+path, &buf))
	return w
}

func websiteMailBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return m
}

func smarthostRequest() map[string]any {
	return map[string]any{
		"mode": "smarthost", "host": "smtp.example.com", "port": 587, "tls": "starttls",
		"username": "relay@example.com", "password": "s3cret-pw",
	}
}

func TestWebsiteMail_GetDefaults(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, MailEnabled: true}}
	w := websiteMailCall(t, websiteMailRouter(t, repo, testSSOKey(t), &probeRecorder{}, true), http.MethodGet, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := websiteMailBody(t, w)
	if got["mode"] != "local" || got["port"] != float64(587) || got["tls"] != "starttls" || got["password_set"] != false || got["mail_module_enabled"] != true {
		t.Errorf("GET = %v", got)
	}
	if ports, _ := got["allowed_ports"].([]any); len(ports) != 4 {
		t.Errorf("allowed_ports = %v", got["allowed_ports"])
	}
}

func TestWebsiteMail_SwitchToSmarthostTestsFirstAndSealsThePassword(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, Hostname: "panel.example.com"}}
	key := testSSOKey(t)
	p := &probeRecorder{}
	w := websiteMailCall(t, websiteMailRouter(t, repo, key, p, true), http.MethodPut, "", smarthostRequest())
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(p.calls) != 1 {
		t.Fatalf("probe calls = %d, want the smarthost tested once before saving", len(p.calls))
	}
	want := smarthost.Config{Host: "smtp.example.com", Port: 587, TLS: "starttls", Username: "relay@example.com", Password: "s3cret-pw", HeloName: "panel.example.com"}
	if p.calls[0] != want {
		t.Errorf("probed %+v, want %+v", p.calls[0], want)
	}

	s := repo.getResult
	if s.WebsiteMailMode != "smarthost" || s.SmarthostHost != "smtp.example.com" || s.SmarthostPort != 587 || s.SmarthostTLS != "starttls" || s.SmarthostUsername != "relay@example.com" {
		t.Errorf("saved %+v", s)
	}
	if len(s.SmarthostPasswordEnc) == 0 || bytes.Contains(s.SmarthostPasswordEnc, []byte("s3cret-pw")) {
		t.Fatalf("password stored as %q, want it sealed", s.SmarthostPasswordEnc)
	}
	if pt, err := key.Open(s.SmarthostPasswordEnc); err != nil || string(pt) != "s3cret-pw" {
		t.Errorf("sealed password opens to %q, %v", pt, err)
	}
	if strings.Contains(w.Body.String(), "s3cret-pw") {
		t.Fatal("the response echoed the password")
	}
	if got := websiteMailBody(t, w); got["password_set"] != true || got["mode"] != "smarthost" {
		t.Errorf("PUT response = %v", got)
	}

	// GET never returns it either.
	g := websiteMailCall(t, websiteMailRouter(t, repo, key, p, true), http.MethodGet, "", nil)
	if strings.Contains(g.Body.String(), "s3cret-pw") || strings.Contains(g.Body.String(), "password\":") {
		t.Fatalf("GET leaks the password: %s", g.Body.String())
	}
}

func TestWebsiteMail_FailedTestSavesNothing(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1}, upsertErr: errMustNotPersist}
	p := &probeRecorder{err: &smarthost.Error{Stage: smarthost.StageAuth, Err: errors.New("535 5.7.8 Authentication credentials invalid")}}
	w := websiteMailCall(t, websiteMailRouter(t, repo, testSSOKey(t), p, true), http.MethodPut, "", smarthostRequest())
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := websiteMailBody(t, w)
	if got["error"] != "smarthost_test_failed" || got["stage"] != "auth" || !strings.Contains(got["detail"].(string), "535") {
		t.Errorf("body = %v", got)
	}
}

func TestWebsiteMail_EmptyPasswordKeepsTheStoredOne(t *testing.T) {
	key := testSSOKey(t)
	sealed, err := key.Seal([]byte("stored-pw"))
	if err != nil {
		t.Fatal(err)
	}
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{
		ID: 1, WebsiteMailMode: "smarthost", SmarthostHost: "smtp.example.com", SmarthostPort: 587,
		SmarthostTLS: "starttls", SmarthostUsername: "relay@example.com", SmarthostPasswordEnc: sealed,
	}}
	p := &probeRecorder{}
	req := smarthostRequest()
	req["password"] = ""
	req["port"] = 465
	req["tls"] = "tls"
	w := websiteMailCall(t, websiteMailRouter(t, repo, key, p, true), http.MethodPut, "", req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(p.calls) != 1 || p.calls[0].Password != "stored-pw" {
		t.Fatalf("probe got %+v, want the stored password", p.calls)
	}
	if !bytes.Equal(repo.getResult.SmarthostPasswordEnc, sealed) || repo.getResult.SmarthostPort != 465 {
		t.Errorf("saved %+v, want the same sealed password and the new port", repo.getResult)
	}
}

func TestWebsiteMail_ClearingTheUsernameDropsThePassword(t *testing.T) {
	key := testSSOKey(t)
	sealed, _ := key.Seal([]byte("stored-pw"))
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{
		ID: 1, WebsiteMailMode: "smarthost", SmarthostHost: "10.0.0.25", SmarthostPort: 587,
		SmarthostTLS: "starttls", SmarthostUsername: "relay", SmarthostPasswordEnc: sealed,
	}}
	p := &probeRecorder{}
	w := websiteMailCall(t, websiteMailRouter(t, repo, key, p, true), http.MethodPut, "",
		map[string]any{"mode": "smarthost", "host": "10.0.0.25", "port": 25, "tls": "none"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if p.calls[0].Username != "" || p.calls[0].Password != "" {
		t.Errorf("probed with a login: %+v", p.calls[0])
	}
	if repo.getResult.SmarthostUsername != "" || repo.getResult.SmarthostPasswordEnc != nil {
		t.Errorf("saved %+v, want no login", repo.getResult)
	}
}

func TestWebsiteMail_BackToLocalClearsTheSmarthostWithoutTesting(t *testing.T) {
	key := testSSOKey(t)
	sealed, _ := key.Seal([]byte("stored-pw"))
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{
		ID: 1, WebsiteMailMode: "smarthost", SmarthostHost: "smtp.example.com", SmarthostPort: 465,
		SmarthostTLS: "tls", SmarthostUsername: "relay", SmarthostPasswordEnc: sealed,
	}}
	p := &probeRecorder{err: errors.New("must not be called")}
	w := websiteMailCall(t, websiteMailRouter(t, repo, key, p, true), http.MethodPut, "", map[string]any{"mode": "local"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	s := repo.getResult
	if len(p.calls) != 0 || s.WebsiteMailMode != "local" || s.SmarthostHost != "" || s.SmarthostUsername != "" || s.SmarthostPasswordEnc != nil || s.SmarthostPort != 587 || s.SmarthostTLS != "starttls" {
		t.Errorf("probe calls %d, saved %+v", len(p.calls), s)
	}
}

// A smarthost can be filled in and kept while local mail is still in use; it
// is validated, but only tested when it is switched to.
func TestWebsiteMail_LocalWithASmarthostSavedForLater(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1}}
	p := &probeRecorder{err: errors.New("must not be called")}
	req := smarthostRequest()
	req["mode"] = "local"
	w := websiteMailCall(t, websiteMailRouter(t, repo, testSSOKey(t), p, true), http.MethodPut, "", req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(p.calls) != 0 || repo.getResult.WebsiteMailMode != "local" || repo.getResult.SmarthostHost != "smtp.example.com" || len(repo.getResult.SmarthostPasswordEnc) == 0 {
		t.Errorf("probe calls %d, saved %+v", len(p.calls), repo.getResult)
	}

	req["port"] = 8080
	w = websiteMailCall(t, websiteMailRouter(t, repo, testSSOKey(t), p, true), http.MethodPut, "", req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an invalid smarthost saved while local: %d %s", w.Code, w.Body.String())
	}
}

func TestWebsiteMail_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edit     func(map[string]any)
		key      bool
		wantCode int
		wantErr  string
	}{
		{"unknown mode", func(r map[string]any) { r["mode"] = "relay" }, true, http.StatusUnprocessableEntity, "invalid_mode"},
		{"a non-mail port", func(r map[string]any) { r["port"] = 22 }, true, http.StatusUnprocessableEntity, "invalid_smarthost"},
		{"a login over plaintext", func(r map[string]any) { r["tls"] = "none" }, true, http.StatusUnprocessableEntity, "invalid_smarthost"},
		{"a URL as host", func(r map[string]any) { r["host"] = "https://smtp.example.com" }, true, http.StatusUnprocessableEntity, "invalid_smarthost"},
		{"a username without any password", func(r map[string]any) { r["password"] = "" }, true, http.StatusUnprocessableEntity, "invalid_smarthost"},
		{"no sso.key to seal the password", func(map[string]any) {}, false, http.StatusServiceUnavailable, "sso_key_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1}, upsertErr: errMustNotPersist}
			var key *ssokey.Key
			if tc.key {
				key = testSSOKey(t)
			}
			req := smarthostRequest()
			tc.edit(req)
			p := &probeRecorder{}
			w := websiteMailCall(t, websiteMailRouter(t, repo, key, p, true), http.MethodPut, "", req)
			if w.Code != tc.wantCode {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if got := websiteMailBody(t, w)["error"]; got != tc.wantErr {
				t.Errorf("error = %v, want %s", got, tc.wantErr)
			}
			if tc.wantErr != "invalid_mode" && len(p.calls) != 0 {
				t.Error("the smarthost was dialed for a request that can't be saved")
			}
		})
	}
}

func TestWebsiteMail_TestUsesTheFormAndSavesNothing(t *testing.T) {
	key := testSSOKey(t)
	sealed, _ := key.Seal([]byte("stored-pw"))
	repo := &mockServerSettingsRepo{
		getResult: &models.ServerSettings{ID: 1, Hostname: "panel.example.com", SmarthostHost: "smtp.example.com", SmarthostUsername: "relay@example.com", SmarthostPasswordEnc: sealed},
		upsertErr: errMustNotPersist,
	}
	p := &probeRecorder{}
	req := smarthostRequest()
	req["password"] = ""
	w := websiteMailCall(t, websiteMailRouter(t, repo, key, p, true), http.MethodPost, "/test", req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(p.calls) != 1 || p.calls[0].Password != "stored-pw" || p.calls[0].HeloName != "panel.example.com" {
		t.Errorf("probe got %+v", p.calls)
	}

	p.err = &smarthost.Error{Stage: smarthost.StageTLS, Err: errors.New("the smarthost doesn't offer STARTTLS")}
	w = websiteMailCall(t, websiteMailRouter(t, repo, key, p, true), http.MethodPost, "/test", req)
	got := websiteMailBody(t, w)
	if w.Code != http.StatusUnprocessableEntity || got["stage"] != "tls" || !strings.Contains(got["detail"].(string), "STARTTLS") {
		t.Errorf("failed test: %d %v", w.Code, got)
	}
}

func TestWebsiteMail_AdminOnly(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1}, upsertErr: errMustNotPersist}
	p := &probeRecorder{}
	r := websiteMailRouter(t, repo, testSSOKey(t), p, false)
	for _, call := range []struct{ method, path string }{{http.MethodGet, ""}, {http.MethodPut, ""}, {http.MethodPost, "/test"}} {
		if w := websiteMailCall(t, r, call.method, call.path, smarthostRequest()); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a tenant: %d", call.method, call.path, w.Code)
		}
	}
	if len(p.calls) != 0 {
		t.Error("a tenant made the panel dial a smarthost")
	}
}

// The stored password is only sent to the host and username it was saved
// with. A Test or save that points an empty password at another host must not
// log in there with the stored one (that would hand it to whoever runs the
// host), and must ask for the password again instead.
func TestWebsiteMail_StoredPasswordNeverGoesToANewHost(t *testing.T) {
	key := testSSOKey(t)
	sealed, _ := key.Seal([]byte("stored-pw"))
	stored := func() *models.ServerSettings {
		return &models.ServerSettings{
			ID: 1, WebsiteMailMode: "smarthost", SmarthostHost: "smtp.example.com", SmarthostPort: 587,
			SmarthostTLS: "starttls", SmarthostUsername: "relay@example.com", SmarthostPasswordEnc: sealed,
		}
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"another host", func(r map[string]any) { r["host"] = "smtp.attacker.example" }},
		{"another username", func(r map[string]any) { r["username"] = "someone@example.com" }},
	} {
		for _, call := range []struct{ method, path string }{{http.MethodPost, "/test"}, {http.MethodPut, ""}} {
			t.Run(tc.name+" "+call.method, func(t *testing.T) {
				repo := &mockServerSettingsRepo{getResult: stored(), upsertErr: errMustNotPersist}
				p := &probeRecorder{}
				req := smarthostRequest()
				req["password"] = ""
				tc.edit(req)
				w := websiteMailCall(t, websiteMailRouter(t, repo, key, p, true), call.method, call.path, req)
				if w.Code != http.StatusUnprocessableEntity || websiteMailBody(t, w)["error"] != "password_required" {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				for _, c := range p.calls {
					if c.Password == "stored-pw" {
						t.Fatalf("the stored password was sent to %s as %s", c.Host, c.Username)
					}
				}
			})
		}
	}

	// The same host in another letter case is the same host.
	repo := &mockServerSettingsRepo{getResult: stored(), upsertErr: errMustNotPersist}
	p := &probeRecorder{}
	req := smarthostRequest()
	req["password"] = ""
	req["host"] = "SMTP.Example.com"
	if w := websiteMailCall(t, websiteMailRouter(t, repo, key, p, true), http.MethodPost, "/test", req); w.Code != http.StatusOK || p.calls[0].Password != "stored-pw" {
		t.Fatalf("same host, other case: %d %s", w.Code, w.Body.String())
	}
}
