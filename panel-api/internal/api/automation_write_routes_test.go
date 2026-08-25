package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes ---

type awAgent struct {
	calls []string
	err   error
}

func (a *awAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	if a.err != nil {
		return nil, a.err
	}
	return json.RawMessage(`{}`), nil
}

type awUsers struct {
	repository.UserRepository
	u         *models.User
	suspended *bool
}

func (r *awUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if r.u != nil && r.u.ID == id {
		return r.u, nil
	}
	return nil, repository.ErrNotFound
}
func (r *awUsers) SetSuspended(_ context.Context, _ string, s bool, _ string) error {
	r.suspended = &s
	return nil
}

type awDomains struct {
	repository.DomainRepository
	d       *models.Domain
	updated bool
}

func (r *awDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if r.d != nil && r.d.ID == id {
		return r.d, nil
	}
	return nil, repository.ErrNotFound
}
func (r *awDomains) FindByName(_ context.Context, n string) (*models.Domain, error) {
	if r.d != nil && r.d.Name == n {
		return r.d, nil
	}
	return nil, repository.ErrNotFound
}
func (r *awDomains) Update(_ context.Context, _ *models.Domain) error { r.updated = true; return nil }

type awOps struct {
	created []*models.AutomationOperation
	prior   *models.AutomationOperation
}

func (r *awOps) Create(_ context.Context, op *models.AutomationOperation) error {
	r.created = append(r.created, op)
	return nil
}
func (r *awOps) FindByID(_ context.Context, id string) (*models.AutomationOperation, error) {
	for _, o := range r.created {
		if o.ID == id {
			return o, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *awOps) FindByIdempotencyKey(_ context.Context, _, _ string) (*models.AutomationOperation, error) {
	if r.prior != nil {
		return r.prior, nil
	}
	return nil, repository.ErrNotFound
}
func (r *awOps) SetStatus(_ context.Context, _, _, _ string) error { return nil }

type awAudit struct {
	repository.AuditEventRepository
	rows []*models.AuditEvent
}

func (r *awAudit) Create(_ context.Context, e *models.AuditEvent) error {
	r.rows = append(r.rows, e)
	return nil
}

func awRouter(cfg AutomationConfig, tok *models.AutomationToken) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("")
	grp.Use(func(c *gin.Context) { c.Set("jabali_automation_token", tok); c.Next() })
	registerAutomationWrites(grp, cfg)
	return r
}

func awPost(r *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func writeTok() *models.AutomationToken {
	return &models.AutomationToken{ID: "tokW", WritesEnabled: true, Scopes: models.AutomationScopes{"write:*"}}
}

func TestServicesRestart_OKAudits(t *testing.T) {
	ag := &awAgent{}
	aud := &awAudit{}
	r := awRouter(AutomationConfig{Agent: ag, Audits: aud}, writeTok())
	w := awPost(r, "/services/nginx/restart")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(ag.calls) != 1 || ag.calls[0] != "service.restart" {
		t.Fatalf("agent call = %v", ag.calls)
	}
	if len(aud.rows) != 1 || aud.rows[0].Result != models.AuditResultOK || aud.rows[0].ActorKind != models.AuditActorAutomation {
		t.Fatalf("expected one automation audit row, got %+v", aud.rows)
	}
}

func TestUserDisable_AdminConflict(t *testing.T) {
	users := &awUsers{u: &models.User{ID: "u1", IsAdmin: true}}
	aud := &awAudit{}
	r := awRouter(AutomationConfig{Users: users, Audits: aud}, writeTok())
	w := awPost(r, "/users/u1/disable")
	if w.Code != http.StatusConflict {
		t.Fatalf("disabling admin: want 409, got %d", w.Code)
	}
	if users.suspended != nil {
		t.Fatal("must not call SetSuspended on an admin")
	}
}

func TestUserDisable_NotFound(t *testing.T) {
	r := awRouter(AutomationConfig{Users: &awUsers{}, Audits: &awAudit{}}, writeTok())
	if code := awPost(r, "/users/ghost/disable").Code; code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", code)
	}
}

// TestUserDisable_RoutesLifecycle proves automation disable runs the full
// User Account Lifecycle transition (JAB-281) — not a bare suspended-flag flip.
// With a Linux username + agent wired, userops.Suspend must lock OS + FTP
// credentials; a bare SetSuspended would leave agent.calls empty.
func TestUserDisable_RoutesLifecycle(t *testing.T) {
	uname := "alice"
	users := &awUsers{u: &models.User{ID: "u1", Email: "a@x.tld", Username: &uname}}
	ag := &awAgent{}
	aud := &awAudit{}
	r := awRouter(AutomationConfig{Users: users, Agent: ag, Audits: aud}, writeTok())
	w := awPost(r, "/users/u1/disable")
	if w.Code != http.StatusOK {
		t.Fatalf("disable: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if users.suspended == nil || !*users.suspended {
		t.Fatal("suspended flag was not set true")
	}
	if !awCalled(ag, "user.suspend") || !awCalled(ag, "ftpaccount.lock_tenant") {
		t.Fatalf("lifecycle OS/FTP lock did not fire; agent calls = %v", ag.calls)
	}
}

// TestUserEnable_RoutesLifecycle is the unsuspend mirror: the OS unlock agent
// command must fire, proving enable also routes through userops.Unsuspend.
func TestUserEnable_RoutesLifecycle(t *testing.T) {
	uname := "alice"
	users := &awUsers{u: &models.User{ID: "u1", Email: "a@x.tld", Username: &uname, Suspended: true}}
	ag := &awAgent{}
	r := awRouter(AutomationConfig{Users: users, Agent: ag, Audits: &awAudit{}}, writeTok())
	w := awPost(r, "/users/u1/enable")
	if w.Code != http.StatusOK {
		t.Fatalf("enable: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if users.suspended == nil || *users.suspended {
		t.Fatal("suspended flag was not cleared")
	}
	if !awCalled(ag, "user.unsuspend") {
		t.Fatalf("lifecycle OS unlock did not fire; agent calls = %v", ag.calls)
	}
}

func awCalled(a *awAgent, cmd string) bool {
	for _, c := range a.calls {
		if c == cmd {
			return true
		}
	}
	return false
}

func TestDomainSuspend_IdempotentNoop(t *testing.T) {
	// Domain already disabled → suspend is a no-op 200, no Update call.
	dom := &awDomains{d: &models.Domain{ID: "d1", Name: "x.test", IsEnabled: false}}
	r := awRouter(AutomationConfig{Domains: dom, Audits: &awAudit{}}, writeTok())
	w := awPost(r, "/domains/d1/suspend")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if dom.updated {
		t.Fatal("idempotent no-op must not call Update")
	}
}

func TestDomainSuspend_FlipsEnabled(t *testing.T) {
	dom := &awDomains{d: &models.Domain{ID: "d1", Name: "x.test", IsEnabled: true}}
	r := awRouter(AutomationConfig{Domains: dom, Audits: &awAudit{}}, writeTok())
	if code := awPost(r, "/domains/d1/suspend").Code; code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if !dom.updated || dom.d.IsEnabled {
		t.Fatalf("suspend must set is_enabled=false via Update (updated=%v enabled=%v)", dom.updated, dom.d.IsEnabled)
	}
}

func TestBackupEnqueue_202AndOp(t *testing.T) {
	ops := &awOps{}
	r := awRouter(AutomationConfig{Operations: ops, Audits: &awAudit{}}, writeTok())
	w := awPost(r, "/backups")
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["ok"] != true || body["operation_id"] == nil || body["status"] != "pending" {
		t.Fatalf("bad async envelope: %v", body)
	}
	if len(ops.created) != 1 || ops.created[0].Action != "backup" {
		t.Fatalf("expected one backup op, got %+v", ops.created)
	}
}

func TestOperationPoll_CrossTokenHidden(t *testing.T) {
	ops := &awOps{created: []*models.AutomationOperation{{ID: "op1", TokenID: "otherTok", Status: "running"}}}
	r := awRouter(AutomationConfig{Operations: ops, Audits: &awAudit{}}, writeTok())
	req := httptest.NewRequest(http.MethodGet, "/operations/op1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("polling another token's op: want 404, got %d", w.Code)
	}
}

func TestCapabilities_ReflectsMounted(t *testing.T) {
	// Only Agent wired → services.restart present, users/domains/backups absent.
	r := awRouter(AutomationConfig{Agent: &awAgent{}}, writeTok())
	req := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var body struct {
		Actions []capability `json:"actions"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	has := map[string]bool{}
	for _, a := range body.Actions {
		has[a.Action] = true
	}
	if !has["services.restart"] {
		t.Fatal("services.restart should be listed when Agent is wired")
	}
	if has["users.disable"] || has["backups.create"] {
		t.Fatal("unmounted actions must not appear in capabilities")
	}
}

type awNotifier struct{ kinds []string }

func (n *awNotifier) Publish(_ context.Context, env notifications.Envelope) (string, error) {
	n.kinds = append(n.kinds, env.EventKind)
	return "sid", nil
}

func TestDomainSuspend_Notifies(t *testing.T) {
	dom := &awDomains{d: &models.Domain{ID: "d1", Name: "x.test", IsEnabled: true}}
	nf := &awNotifier{}
	r := awRouter(AutomationConfig{Domains: dom, Audits: &awAudit{}, Notify: nf}, writeTok())
	if code := awPost(r, "/domains/d1/suspend").Code; code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if len(nf.kinds) != 1 || nf.kinds[0] != "automation.domain.suspended" {
		t.Fatalf("expected an automation.domain.suspended M14 event, got %v", nf.kinds)
	}
}

func TestDomainSuspend_IdempotentNoop_NoNotify(t *testing.T) {
	// Already suspended → no-op → must NOT re-notify.
	dom := &awDomains{d: &models.Domain{ID: "d1", Name: "x.test", IsEnabled: false}}
	nf := &awNotifier{}
	r := awRouter(AutomationConfig{Domains: dom, Audits: &awAudit{}, Notify: nf}, writeTok())
	awPost(r, "/domains/d1/suspend")
	if len(nf.kinds) != 0 {
		t.Fatalf("idempotent no-op must not notify, got %v", nf.kinds)
	}
}
