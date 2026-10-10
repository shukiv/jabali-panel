package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/websitemail"
)

// GH #2056: a save switches the server first (agent mail.relay.apply), so a
// save the server couldn't apply changes nothing, and the server never runs
// on settings the panel doesn't have.

type wmUsers struct {
	repository.UserRepository
	rows []models.User
}

func (s wmUsers) FindByIDs(_ context.Context, ids []string) ([]models.User, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []models.User
	for _, u := range s.rows {
		if want[u.ID] {
			out = append(out, u)
		}
	}
	return out, nil
}

type wmDomains struct {
	repository.DomainRepository
	rows []models.Domain
}

func (s wmDomains) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	return s.rows, int64(len(s.rows)), nil
}

type wmPackages struct {
	repository.PackageRepository
	rows []models.HostingPackage
}

func (s wmPackages) List(context.Context, repository.ListOptions) ([]models.HostingPackage, int64, error) {
	return s.rows, int64(len(s.rows)), nil
}

func wmSites() (wmUsers, wmDomains, wmPackages) {
	alice, pkg := "alice", "p1"
	return wmUsers{rows: []models.User{{ID: "u1", Username: &alice, PackageID: &pkg}}},
		wmDomains{rows: []models.Domain{{ID: "d1", UserID: "u1", Name: "alice.example", IsEnabled: true,
			OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}}}},
		wmPackages{rows: []models.HostingPackage{{ID: "p1", WebsiteSendsEmail: true}}}
}

func websiteMailApplyRouter(t *testing.T, repo *mockServerSettingsRepo, key *ssokey.Key, a *agent.MockClient) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
	})
	users, domains, packages := wmSites()
	RegisterWebsiteMailRoutes(r.Group("/api/v1"), WebsiteMailHandlerConfig{
		Repo: repo, SSOKey: key, Probe: (&probeRecorder{}).probe,
		Agent: a, Users: users, Domains: domains, Packages: packages,
	})
	return r
}

func relayCalls(a *agent.MockClient) []websitemail.ApplyRequest {
	var out []websitemail.ApplyRequest
	for _, c := range a.Calls() {
		if c.Command != "mail.relay.apply" {
			continue
		}
		var req websitemail.ApplyRequest
		_ = json.Unmarshal(c.Params, &req)
		out = append(out, req)
	}
	return out
}

func TestWebsiteMail_SaveSwitchesTheServerFirst(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, Hostname: "panel.example.com"}}
	a := agent.NewMockClient().On("mail.relay.apply", websitemail.ApplyResponse{Ok: true, Mode: "smarthost", Changed: true, Senders: 1, Skipped: []string{"ghost: no such system user"}})
	w := websiteMailCall(t, websiteMailApplyRouter(t, repo, testSSOKey(t), a), http.MethodPut, "", smarthostRequest())
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	calls := relayCalls(a)
	if len(calls) != 1 {
		t.Fatalf("mail.relay.apply calls = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.Mode != "smarthost" || got.Smarthost == nil || got.Smarthost.Host != "smtp.example.com" || got.Smarthost.Password != "s3cret-pw" || got.Smarthost.Helo != "panel.example.com" {
		t.Errorf("applied %+v / %+v", got, got.Smarthost)
	}
	if len(got.Senders) != 1 || got.Senders[0].Username != "alice" {
		t.Errorf("senders = %+v", got.Senders)
	}
	body := websiteMailBody(t, w)
	if body["senders"] != float64(1) || !strings.Contains(w.Body.String(), "ghost: no such system user") {
		t.Errorf("response = %s, want the sender count and who was left out", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "s3cret-pw") {
		t.Fatal("the response echoed the password")
	}
}

func TestWebsiteMail_SaveTheServerCouldntApplyChangesNothing(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, Hostname: "panel.example.com"}, upsertErr: errMustNotPersist}
	a := agent.NewMockClient().OnError("mail.relay.apply", &agent.AgentError{Code: "internal", Message: "the mail relay didn't start; see journalctl -u jabali-mailrelay.service"})
	w := websiteMailCall(t, websiteMailApplyRouter(t, repo, testSSOKey(t), a), http.MethodPut, "", smarthostRequest())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "the mail relay didn't start") {
		t.Errorf("body = %s, want the server's reason", w.Body.String())
	}
	if repo.getResult.WebsiteMailMode != "" {
		t.Errorf("saved %+v although the server wasn't switched", repo.getResult)
	}
}

func TestWebsiteMail_BackToLocalSendsNoPassword(t *testing.T) {
	key := testSSOKey(t)
	sealed, _ := key.Seal([]byte("old-pw"))
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, WebsiteMailMode: "smarthost", SmarthostHost: "smtp.example.com", SmarthostPort: 587, SmarthostTLS: "starttls", SmarthostUsername: "relay", SmarthostPasswordEnc: sealed}}
	a := agent.NewMockClient().On("mail.relay.apply", websitemail.ApplyResponse{Ok: true, Mode: "local", Changed: true})
	w := websiteMailCall(t, websiteMailApplyRouter(t, repo, key, a), http.MethodPut, "", map[string]any{"mode": "local"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	raw := a.Calls()
	if len(raw) != 1 || string(raw[0].Params) != `{"mode":"local"}` {
		t.Errorf("agent calls = %+v, want only {\"mode\":\"local\"}", raw)
	}
}

func TestWebsiteMail_FailedSavePutsTheServerBack(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, Hostname: "panel.example.com"}, upsertErr: errors.New("db down")}
	a := agent.NewMockClient().On("mail.relay.apply", websitemail.ApplyResponse{Ok: true})
	w := websiteMailCall(t, websiteMailApplyRouter(t, repo, testSSOKey(t), a), http.MethodPut, "", smarthostRequest())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	calls := relayCalls(a)
	if len(calls) != 2 || calls[0].Mode != "smarthost" || calls[1].Mode != "local" {
		t.Errorf("applied %+v, want the smarthost and then the saved local mode back", calls)
	}
}

func TestWebsiteMail_GetCountsSenders(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, WebsiteMailMode: "smarthost", SmarthostHost: "smtp.example.com", SmarthostPort: 25, SmarthostTLS: "none"}}
	w := websiteMailCall(t, websiteMailApplyRouter(t, repo, testSSOKey(t), agent.NewMockClient()), http.MethodGet, "", nil)
	if got := websiteMailBody(t, w); got["senders"] != float64(1) {
		t.Errorf("GET = %v, want senders 1", got)
	}
}
