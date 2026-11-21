package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailcreds"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// The mailbox password page (GH #2543): a mailbox user changes their own
// password by proving the current one.

type mpwMailboxRepo struct {
	repository.MailboxRepository
	mb       *models.Mailbox
	lookedUp bool
	newHash  string
}

func (r *mpwMailboxRepo) FindByEmail(_ context.Context, email string) (*models.Mailbox, error) {
	r.lookedUp = true
	if r.mb != nil && email == r.mb.EmailCached {
		cp := *r.mb
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (r *mpwMailboxRepo) ReplacePasswordHash(_ context.Context, id, oldHash, newHash string, _ []byte) (bool, error) {
	if r.mb == nil || id != r.mb.ID || oldHash != r.mb.PasswordHash {
		return false, nil
	}
	r.mb.PasswordHash = newHash
	r.newHash = newHash
	return true, nil
}

type mpwDomainRepo struct {
	repository.DomainRepository
	dom *models.Domain
}

func (r *mpwDomainRepo) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if r.dom != nil && r.dom.ID == id {
		return r.dom, nil
	}
	return nil, repository.ErrNotFound
}

type mpwUserRepo struct {
	repository.UserRepository
	u *models.User
}

func (r *mpwUserRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	if r.u != nil && r.u.ID == id {
		return r.u, nil
	}
	return nil, repository.ErrNotFound
}

type mpwSweeper struct{ calls int }

func (s *mpwSweeper) SweepMailCredentials(context.Context) ([]mailcreds.Removal, error) {
	s.calls++
	return []mailcreds.Removal{{Account: "alice@example.com", Type: "AppPassword", Reason: "password changed"}}, nil
}

type mpwAgent struct{ calls []string }

func (a *mpwAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	return json.RawMessage(`{"ok":true}`), nil
}

type mpwLimiter struct {
	allow bool
	seen  []string
}

func (l *mpwLimiter) AllowMailboxPassword(_ *gin.Context, email string, _ *slog.Logger) bool {
	l.seen = append(l.seen, email)
	return l.allow
}

type mpwFixture struct {
	mailboxes *mpwMailboxRepo
	domains   *mpwDomainRepo
	users     *mpwUserRepo
	sweeper   *mpwSweeper
	agent     *mpwAgent
	limiter   *mpwLimiter
	cfg       MailboxPasswordPageConfig
}

func newMPWFixture(t *testing.T) *mpwFixture {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte("old-password-1"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	f := &mpwFixture{
		mailboxes: &mpwMailboxRepo{mb: &models.Mailbox{ID: "mb1", DomainID: "d1", EmailCached: "alice@example.com", PasswordHash: string(h)}},
		domains: &mpwDomainRepo{dom: &models.Domain{ID: "d1", Name: "example.com", UserID: "u1",
			OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}}},
		users:   &mpwUserRepo{u: &models.User{ID: "u1"}},
		sweeper: &mpwSweeper{},
		agent:   &mpwAgent{},
		limiter: &mpwLimiter{allow: true},
	}
	f.cfg = MailboxPasswordPageConfig{
		Mailboxes: f.mailboxes, Domains: f.domains, Users: f.users,
		MailCredentials: f.sweeper, Agent: f.agent, Limiter: f.limiter,
	}
	return f
}

func (f *mpwFixture) do(method, target, contentType, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterMailboxPasswordRoutes(r, f.cfg)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.RemoteAddr = "203.0.113.7:4444"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const formType = "application/x-www-form-urlencoded"

func mpwForm(email, current, newPw, confirm string) string {
	return url.Values{"email": {email}, "current_password": {current}, "new_password": {newPw}, "confirm_password": {confirm}}.Encode()
}

func TestMailboxPasswordPage_GetServesTheFormWithLockedDownHeaders(t *testing.T) {
	f := newMPWFixture(t)
	w := f.do(http.MethodGet, "/mailbox-password", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	for k, want := range map[string]string{
		"Cache-Control":   "no-store",
		"X-Frame-Options": "DENY",
		"Referrer-Policy": "no-referrer",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, d := range []string{"default-src 'none'", "form-action 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, d) {
			t.Errorf("CSP %q lacks %q", csp, d)
		}
	}
	if !strings.Contains(w.Body.String(), `name="current_password"`) {
		t.Fatal("the form is missing")
	}
}

func TestMailboxPasswordPage_ChangesThePassword(t *testing.T) {
	f := newMPWFixture(t)
	w := f.do(http.MethodPost, "/mailbox-password", formType, mpwForm("Alice@Example.com", "old-password-1", "new-password-2", "new-password-2"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if bcrypt.CompareHashAndPassword([]byte(f.mailboxes.newHash), []byte("new-password-2")) != nil {
		t.Fatal("the stored hash is not the new password's")
	}
	if f.sweeper.calls != 1 {
		t.Fatalf("app passwords sweep ran %d times, want 1", f.sweeper.calls)
	}
	if strings.Join(f.agent.calls, ",") != "mailbox.set_password,mail.auth_cache.flush" {
		t.Fatalf("agent calls = %v", f.agent.calls)
	}
	if !strings.Contains(w.Body.String(), "Your password was changed") {
		t.Fatal("no success message")
	}
}

func TestMailboxPasswordPage_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		edit   func(*mpwFixture)
		ctype  string
		body   string
		status int
		lookup bool // whether the mailbox may be looked up
	}{
		{name: "wrong current password", body: mpwForm("alice@example.com", "wrong-one-1", "new-password-2", "new-password-2"),
			status: http.StatusBadRequest, lookup: true},
		{name: "unknown address", body: mpwForm("bob@example.com", "old-password-1", "new-password-2", "new-password-2"),
			status: http.StatusBadRequest, lookup: true},
		{name: "domain ownership pending",
			edit:   func(f *mpwFixture) { f.domains.dom.OwnershipStatus = models.OwnershipPending },
			body:   mpwForm("alice@example.com", "old-password-1", "new-password-2", "new-password-2"),
			status: http.StatusBadRequest, lookup: true},
		{name: "owner suspended",
			edit:   func(f *mpwFixture) { f.users.u.Suspended = true },
			body:   mpwForm("alice@example.com", "old-password-1", "new-password-2", "new-password-2"),
			status: http.StatusBadRequest, lookup: true},
		{name: "owner missing",
			edit:   func(f *mpwFixture) { f.users.u = nil },
			body:   mpwForm("alice@example.com", "old-password-1", "new-password-2", "new-password-2"),
			status: http.StatusBadRequest, lookup: true},
		{name: "over the rate limit",
			edit:   func(f *mpwFixture) { f.limiter.allow = false },
			body:   mpwForm("alice@example.com", "old-password-1", "new-password-2", "new-password-2"),
			status: http.StatusTooManyRequests},
		{name: "confirmation mismatch", body: mpwForm("alice@example.com", "old-password-1", "new-password-2", "new-password-3"),
			status: http.StatusBadRequest},
		{name: "new equals current", body: mpwForm("alice@example.com", "old-password-1", "old-password-1", "old-password-1"),
			status: http.StatusBadRequest},
		{name: "weak new password", body: mpwForm("alice@example.com", "old-password-1", "short", "short"),
			status: http.StatusBadRequest},
		{name: "JSON body", ctype: "application/json", body: `{"email":"alice@example.com"}`,
			status: http.StatusUnsupportedMediaType},
		{name: "no limiter wired",
			edit:   func(f *mpwFixture) { f.cfg.Limiter = nil },
			body:   mpwForm("alice@example.com", "old-password-1", "new-password-2", "new-password-2"),
			status: http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMPWFixture(t)
			if tc.edit != nil {
				tc.edit(f)
			}
			ct := tc.ctype
			if ct == "" {
				ct = formType
			}
			w := f.do(http.MethodPost, "/mailbox-password", ct, tc.body)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.status, w.Body.String())
			}
			if f.mailboxes.newHash != "" {
				t.Fatal("a refused change wrote a new hash")
			}
			if !tc.lookup && f.mailboxes.lookedUp {
				t.Fatal("the mailbox was looked up although the request was refused before any password check")
			}
			if f.sweeper.calls != 0 || len(f.agent.calls) != 0 {
				t.Fatal("a refused change swept credentials or called the agent")
			}
		})
	}
}

// Wrong password, unknown address and an ineligible mailbox read the same.
func TestMailboxPasswordPage_RefusalsReadTheSame(t *testing.T) {
	bodies := map[string]string{}
	for name, edit := range map[string]func(*mpwFixture){
		"wrong":     nil,
		"unknown":   func(f *mpwFixture) { f.mailboxes.mb.EmailCached = "someone-else@example.com" },
		"suspended": func(f *mpwFixture) { f.users.u.Suspended = true },
	} {
		f := newMPWFixture(t)
		if edit != nil {
			edit(f)
		}
		pw := "old-password-1"
		if name == "wrong" {
			pw = "wrong-one-1"
		}
		w := f.do(http.MethodPost, "/mailbox-password", formType, mpwForm("alice@example.com", pw, "new-password-2", "new-password-2"))
		bodies[name] = w.Body.String()
	}
	if bodies["wrong"] != bodies["unknown"] || bodies["wrong"] != bodies["suspended"] {
		t.Fatal("the refusals differ, so the page tells a guesser which one it was")
	}
}

func TestMailboxPasswordPage_EscapesTheEchoedAddress(t *testing.T) {
	f := newMPWFixture(t)
	w := f.do(http.MethodPost, "/mailbox-password", formType, mpwForm(`"><script>alert(1)</script>`, "x-password-1", "new-password-2", "new-password-3"))
	if strings.Contains(w.Body.String(), "<script>alert(1)") {
		t.Fatal("the echoed address is not escaped")
	}
}

// Passwords are read from the body only, never the query string.
func TestMailboxPasswordPage_IgnoresQueryStringFields(t *testing.T) {
	f := newMPWFixture(t)
	q := "?" + mpwForm("alice@example.com", "old-password-1", "new-password-2", "new-password-2")
	w := f.do(http.MethodPost, "/mailbox-password"+q, formType, "")
	if w.Code == http.StatusOK || f.mailboxes.newHash != "" {
		t.Fatal("a change was made from query-string fields")
	}
}

func TestMailboxPasswordPage_LimiterSeesTheFormAddress(t *testing.T) {
	f := newMPWFixture(t)
	f.do(http.MethodPost, "/mailbox-password", formType, mpwForm(" alice@example.com ", "old-password-1", "new-password-2", "new-password-2"))
	if len(f.limiter.seen) != 1 || f.limiter.seen[0] != "alice@example.com" {
		t.Fatalf("limiter saw %q, want the form's address", f.limiter.seen)
	}
}
