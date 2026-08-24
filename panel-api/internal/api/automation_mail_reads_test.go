package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes (interface-embedding: only the read method each endpoint calls) ---

type fakeFwdRepo struct {
	repository.EmailForwarderRepository
	rows []models.EmailForwarder
}

func (f fakeFwdRepo) ListAll(context.Context, repository.ListOptions) ([]models.EmailForwarder, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

type fakeMGRepo struct {
	repository.MailGroupRepository
	rows []repository.MailGroupWithDomain
}

func (f fakeMGRepo) ListAllWithDomain(context.Context) ([]repository.MailGroupWithDomain, error) {
	return f.rows, nil
}

type fakeARRepo struct {
	repository.EmailAutoresponderRepository
	rows []models.EmailAutoresponder
}

func (f fakeARRepo) ListAll(context.Context) ([]models.EmailAutoresponder, error) { return f.rows, nil }

type fakeDomRepo struct {
	repository.DomainRepository
	rows []models.Domain
}

func (f fakeDomRepo) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

type fakeMbxRepo struct {
	repository.MailboxRepository
	rows []repository.MailboxWithDomain
}

func (f fakeMbxRepo) ListAllWithDomain(context.Context) ([]repository.MailboxWithDomain, error) {
	return f.rows, nil
}
func (f fakeMbxRepo) CountAll(context.Context) (int64, error) { return int64(len(f.rows)), nil }

// --- harness ---

func mailReadRouter(cfg AutomationConfig, scope string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("")
	tok := &models.AutomationToken{ID: "tok", Scopes: models.AutomationScopes{scope}}
	grp.Use(func(c *gin.Context) { c.Set("jabali_automation_token", tok); c.Next() })
	registerAutomationMailReads(grp, cfg)
	return r
}

func getMail(r *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sptr(s string) *string { return &s }

func decodeList(t *testing.T, w *httptest.ResponseRecorder) (data []map[string]any, total int) {
	t.Helper()
	var env struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return env.Data, env.Total
}

func fullMailCfg() AutomationConfig {
	return AutomationConfig{
		Mailboxes: fakeMbxRepo{rows: []repository.MailboxWithDomain{
			{Mailbox: models.Mailbox{ID: "mb1", EmailCached: "alice@ex.com"}},
		}},
		Domains: fakeDomRepo{rows: []models.Domain{{ID: "d1", Name: "ex.com", UserID: "u1"}}},
		Forwarders: fakeFwdRepo{rows: []models.EmailForwarder{
			{ID: "f1", MailboxID: sptr("mb1"), DomainID: "d1", Type: "external", LocalPart: sptr("alice"), Target: "a@out.com", Enabled: true},
			{ID: "f2", MailboxID: nil, DomainID: "d1", Type: "alias", LocalPart: sptr("info"), Target: "team@ex.com", Enabled: true},
		}},
		MailGroups: fakeMGRepo{rows: []repository.MailGroupWithDomain{
			{MailGroup: models.MailGroup{ID: "g1", EmailCached: "team@ex.com", HasMailbox: true}, DomainName: "ex.com", UserUsername: "u1", MemberCount: 3},
		}},
		Autoresponders: fakeARRepo{rows: []models.EmailAutoresponder{
			{MailboxID: "mb1", Enabled: true, Subject: sptr("OOO"), TextBody: sptr("secret body")},
		}},
	}
}

// --- tests ---

func TestAutomationMail_Forwarders_SplitByScope(t *testing.T) {
	r := mailReadRouter(fullMailCfg(), "read:mail")

	// mailbox-level: only f1 (MailboxID set).
	w := getMail(r, "/mail/forwarders")
	if w.Code != http.StatusOK {
		t.Fatalf("forwarders code=%d", w.Code)
	}
	data, total := decodeList(t, w)
	if total != 1 || len(data) != 1 || data[0]["id"] != "f1" {
		t.Fatalf("expected only the mailbox-level forwarder f1, got %v", data)
	}
	if data[0]["domain"] != "ex.com" {
		t.Errorf("forwarder should be domain-enriched, got %v", data[0]["domain"])
	}

	// domain-scoped: only f2 (MailboxID nil).
	w = getMail(r, "/mail/domain-forwarders")
	data, total = decodeList(t, w)
	if total != 1 || len(data) != 1 || data[0]["id"] != "f2" {
		t.Fatalf("expected only the domain-scoped forwarder f2, got %v", data)
	}
	if _, hasMbx := data[0]["mailbox_id"]; hasMbx {
		t.Errorf("domain-scoped forwarder must not carry mailbox_id")
	}
}

func TestAutomationMail_Groups(t *testing.T) {
	r := mailReadRouter(fullMailCfg(), "read:mail")
	w := getMail(r, "/mail/groups")
	if w.Code != http.StatusOK {
		t.Fatalf("groups code=%d", w.Code)
	}
	data, total := decodeList(t, w)
	if total != 1 || len(data) != 1 {
		t.Fatalf("expected 1 group, got %v", data)
	}
	if data[0]["member_count"].(float64) != 3 || data[0]["has_mailbox"] != true {
		t.Errorf("group flags/count wrong: %v", data[0])
	}
}

func TestAutomationMail_Autoresponders_MetadataOnly(t *testing.T) {
	r := mailReadRouter(fullMailCfg(), "read:mail")
	w := getMail(r, "/mail/autoresponders")
	if w.Code != http.StatusOK {
		t.Fatalf("autoresponders code=%d", w.Code)
	}
	data, total := decodeList(t, w)
	if total != 1 || data[0]["enabled"] != true || data[0]["email"] != "alice@ex.com" {
		t.Fatalf("autoresponder row wrong: %v", data)
	}
	// Bodies must NEVER be exposed to an automation token.
	for _, leaked := range []string{"text_body", "html_body", "subject"} {
		if _, ok := data[0][leaked]; ok {
			t.Errorf("autoresponder must not expose %q to automation", leaked)
		}
	}
}

func TestAutomationMail_MissingScope_403(t *testing.T) {
	r := mailReadRouter(fullMailCfg(), "read:domains") // wrong scope
	for _, p := range []string{"/mail/forwarders", "/mail/domain-forwarders", "/mail/groups", "/mail/autoresponders"} {
		w := getMail(r, p)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s without read:mail: code=%d, want 403", p, w.Code)
		}
	}
}

func TestAutomationMail_WildcardScopeAllows(t *testing.T) {
	r := mailReadRouter(fullMailCfg(), "read:*")
	if w := getMail(r, "/mail/groups"); w.Code != http.StatusOK {
		t.Fatalf("read:* must cover read:mail, got %d", w.Code)
	}
}

func TestAutomationMail_EmptyResultSets(t *testing.T) {
	cfg := AutomationConfig{
		Mailboxes:      fakeMbxRepo{},
		Domains:        fakeDomRepo{},
		Forwarders:     fakeFwdRepo{},
		MailGroups:     fakeMGRepo{},
		Autoresponders: fakeARRepo{},
	}
	r := mailReadRouter(cfg, "read:mail")
	for _, p := range []string{"/mail/forwarders", "/mail/domain-forwarders", "/mail/groups", "/mail/autoresponders"} {
		w := getMail(r, p)
		if w.Code != http.StatusOK {
			t.Fatalf("%s empty: code=%d", p, w.Code)
		}
		data, total := decodeList(t, w)
		if total != 0 || len(data) != 0 {
			t.Errorf("%s empty must be data:[] total:0, got %v", p, data)
		}
	}
}
