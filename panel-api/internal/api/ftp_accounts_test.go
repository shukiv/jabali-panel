package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeFtpRepo struct {
	repository.FtpAccountRepository
	rows      map[string]*models.FtpAccount
	createErr error
	updateErr error
	onUpdate  func() // test hook fired at the start of Update (e.g. cancel the request ctx)
	uidSeq    uint32
}

func (f *fakeFtpRepo) AllocateUID(_ context.Context) (uint32, error) {
	f.uidSeq++
	return ftpSubaccountUIDMin + f.uidSeq, nil
}

func (f *fakeFtpRepo) SumIsolatedQuotaByUserID(_ context.Context, userID string) (uint64, error) {
	var sum uint64
	for _, r := range f.rows {
		if r.UserID == userID && r.Isolated {
			sum += uint64(r.QuotaMB)
		}
	}
	return sum, nil
}

func newFakeFtpRepo() *fakeFtpRepo {
	return &fakeFtpRepo{rows: map[string]*models.FtpAccount{}}
}

func (f *fakeFtpRepo) Create(_ context.Context, a *models.FtpAccount) error {
	if f.createErr != nil {
		return f.createErr
	}
	for _, r := range f.rows {
		if r.Username == a.Username {
			return repository.ErrConflict
		}
	}
	cp := *a
	f.rows[a.ID] = &cp
	return nil
}

// ReserveWithinCap mirrors the real repo's atomic cap + quota-split + insert
// (JAB-262). The fake is single-goroutine, so it just models the decision.
func (f *fakeFtpRepo) ReserveWithinCap(ctx context.Context, a *models.FtpAccount, maxAccounts int, pkgDiskQuotaMB uint32) error {
	var count int
	for _, r := range f.rows {
		if r.UserID == a.UserID {
			count++
		}
	}
	if count >= maxAccounts {
		return repository.ErrFtpCapExceeded
	}
	if a.Isolated && pkgDiskQuotaMB > 0 {
		used, _ := f.SumIsolatedQuotaByUserID(ctx, a.UserID)
		if used+uint64(a.QuotaMB) > uint64(pkgDiskQuotaMB) {
			return repository.ErrFtpQuotaSplitExceeded
		}
	}
	return f.Create(ctx, a)
}

func (f *fakeFtpRepo) FindByIDAndUserID(_ context.Context, id, userID string) (*models.FtpAccount, error) {
	if r, ok := f.rows[id]; ok && r.UserID == userID {
		cp := *r
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeFtpRepo) ListByUserID(_ context.Context, userID string) ([]models.FtpAccount, error) {
	out := []models.FtpAccount{}
	for _, r := range f.rows {
		if r.UserID == userID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *fakeFtpRepo) List(_ context.Context) ([]models.FtpAccount, error) {
	out := []models.FtpAccount{}
	for _, r := range f.rows {
		out = append(out, *r)
	}
	return out, nil
}

func (f *fakeFtpRepo) Update(_ context.Context, a *models.FtpAccount) error {
	if f.onUpdate != nil {
		f.onUpdate()
	}
	if f.updateErr != nil {
		return f.updateErr
	}
	if r, ok := f.rows[a.ID]; ok {
		r.FTPAccess, r.SFTPAccess, r.IsEnabled, r.HomePath = a.FTPAccess, a.SFTPAccess, a.IsEnabled, a.HomePath
		return nil
	}
	return repository.ErrNotFound
}

func (f *fakeFtpRepo) Delete(_ context.Context, id string) error {
	delete(f.rows, id)
	return nil
}

func (f *fakeFtpRepo) CountByUserID(_ context.Context, userID string) (int64, error) {
	var n int64
	for _, r := range f.rows {
		if r.UserID == userID {
			n++
		}
	}
	return n, nil
}

// ftpMockAgent wires the happy-path agent responses.
func ftpMockAgent() *agent.MockClient {
	return agent.NewMockClient().
		On("ftpaccount.create", map[string]any{"username": "x"}).
		On("ftpaccount.delete", map[string]any{"deleted": true}).
		On("ftpaccount.set_access", map[string]any{}).
		On("ftpaccount.set_password", map[string]any{"updated": true}).
		On("ftpaccount.sshd_sync", map[string]any{"changed": true}).
		On("ssh.user.home_chown", map[string]any{})
}

// ctxAwareAgent is an AgentInterface that RESPECTS context cancellation (the
// MockClient ignores ctx), so a test can prove the update host-apply survives
// request cancellation. It records the commands it accepted.
type ctxAwareAgent struct{ calls []string }

func (a *ctxAwareAgent) Call(ctx context.Context, command string, _ any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.calls = append(a.calls, command)
	return json.RawMessage(`{}`), nil
}

func (a *ctxAwareAgent) called(cmd string) int {
	n := 0
	for _, c := range a.calls {
		if c == cmd {
			n++
		}
	}
	return n
}

// JAB-269: the update handler persists the DB FIRST. A failed persist must not
// have touched the host at all — otherwise a live host change outlives a DB
// that never recorded it.
func TestFtpAPIUpdate_DBErrorMakesNoHostCall(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.rows["acc1"] = &models.FtpAccount{ID: "acc1", UserID: "u1", Username: "shop_dev", IsEnabled: false, SFTPAccess: true}
	repo.updateErr = errors.New("db down")
	mock := ftpMockAgent()
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	rec := doReq(t, r, http.MethodPatch, "/me/ftp-accounts/acc1", `{"is_enabled":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on DB failure, got %d", rec.Code)
	}
	for _, cmd := range []string{"ftpaccount.set_access", "ftpaccount.sshd_sync", "ssh.user.home_chown"} {
		if mockCalled(mock, cmd) != 0 {
			t.Fatalf("host command %s ran despite the DB write failing — not DB-first", cmd)
		}
	}
}

// JAB-269: when the unix-lock (set_access) fails AFTER the DB commit, the row
// must keep its new value (a recorded revocation must never be silently
// reverted), AND sshd_sync must still run — it renders from the now-committed
// DB, so SFTP is revoked even when the unix-lock failed (defense in depth).
func TestFtpAPIUpdate_HostFailureKeepsRowAndSyncs(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.rows["acc1"] = &models.FtpAccount{ID: "acc1", UserID: "u1", Username: "shop_dev", IsEnabled: true, SFTPAccess: true}
	mock := ftpMockAgent()
	mock.OnError("ftpaccount.set_access", errors.New("agent boom"))
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	rec := doReq(t, r, http.MethodPatch, "/me/ftp-accounts/acc1", `{"is_enabled":false}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("a host-apply failure must surface an error, got 200")
	}
	if repo.rows["acc1"].IsEnabled {
		t.Fatal("row was reverted on host failure — the recorded revocation must survive (JAB-269)")
	}
	if mockCalled(mock, "ftpaccount.sshd_sync") != 1 {
		t.Fatal("sshd_sync must run even when set_access fails (SFTP revocation is defense-in-depth)")
	}
}

// JAB-269: the host apply runs on a context detached from request cancellation.
// Simulate a client disconnect landing exactly at the DB commit; set_access must
// still fire and the request must succeed. (With the old host-first code on the
// request ctx, the cancelled ctx would abort the agent call.)
func TestFtpAPIUpdate_HostApplyDetachedFromCancel(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.rows["acc1"] = &models.FtpAccount{ID: "acc1", UserID: "u1", Username: "shop_dev", IsEnabled: false, SFTPAccess: true}
	ag := &ctxAwareAgent{}

	uname := "shop"
	pkgID := "pkg1"
	users := &usersMap{m: map[string]*models.User{"u1": {ID: "u1", Username: &uname, PackageID: &pkgID}}}
	h := &ftpAccountsHandler{cfg: FtpAccountsHandlerConfig{
		Repo: repo, Users: users, Packages: &fakePkgRepo{pkg: ftpPkg(3)}, Agent: ag, QuotaMount: "/",
	}}
	r := gin.New()
	r.Use(func(c *gin.Context) { ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"}); c.Next() })
	r.PATCH("/me/ftp-accounts/:id", h.update)

	ctx, cancel := context.WithCancel(context.Background())
	repo.onUpdate = cancel // fire the "client disconnect" the instant the DB commits

	req := httptest.NewRequest(http.MethodPatch, "/me/ftp-accounts/acc1", strings.NewReader(`{"is_enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("host apply must survive request cancellation, got %d: %s", rec.Code, rec.Body.String())
	}
	if ag.called("ftpaccount.set_access") != 1 {
		t.Fatal("set_access must fire on the detached ctx despite the request being cancelled")
	}
}

func ftpTestRouter(t *testing.T, repo *fakeFtpRepo, mock *agent.MockClient, pkg *models.HostingPackage) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	uname := "shop"
	pkgID := "pkg1"
	users := &usersMap{m: map[string]*models.User{
		"u1": {ID: "u1", Username: &uname, PackageID: &pkgID},
	}}
	h := &ftpAccountsHandler{cfg: FtpAccountsHandlerConfig{
		Repo:       repo,
		Users:      users,
		Packages:   &fakePkgRepo{pkg: pkg},
		Agent:      mock,
		QuotaMount: "/", // GH #1145: enables isolated create in tests
	}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: false})
		c.Next()
	})
	r.GET("/me/ftp-accounts", h.list)
	r.POST("/me/ftp-accounts", h.create)
	r.PATCH("/me/ftp-accounts/:id", h.update)
	r.POST("/me/ftp-accounts/:id/password", h.setPassword)
	r.DELETE("/me/ftp-accounts/:id", h.delete)
	return r
}

func ftpPkg(maxAccounts uint32) *models.HostingPackage {
	return &models.HostingPackage{ID: "pkg1", Name: "test", MaxFTPAccounts: maxAccounts}
}

func mockCalled(mock *agent.MockClient, command string) int {
	n := 0
	for _, call := range mock.Calls() {
		if call.Command == command {
			n++
		}
	}
	return n
}

// JAB-267: every sshd_sync dispatch must carry a positive generation so the
// agent can reject stale snapshots. A create triggers syncHostAccess.
func TestFtpAPI_SSHDSyncCarriesGeneration(t *testing.T) {
	repo := newFakeFtpRepo()
	mock := ftpMockAgent()
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"deploy","home_path":"/home/shop/site","password":"longenough-pass1","ftp_access":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var found bool
	for _, call := range mock.Calls() {
		if call.Command != "ftpaccount.sshd_sync" {
			continue
		}
		found = true
		var p struct {
			Generation int64 `json:"generation"`
		}
		if err := json.Unmarshal(call.Params, &p); err != nil {
			t.Fatalf("unmarshal sshd_sync params: %v", err)
		}
		if p.Generation <= 0 {
			t.Fatalf("sshd_sync must carry a positive generation, got %d", p.Generation)
		}
	}
	if !found {
		t.Fatal("no ftpaccount.sshd_sync call recorded")
	}
}

func TestFtpAPICreate_HappyPath(t *testing.T) {
	repo := newFakeFtpRepo()
	mock := ftpMockAgent()
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"deploy","home_path":"/home/shop/site","password":"longenough-pass1","ftp_access":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(repo.rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(repo.rows))
	}
	for _, row := range repo.rows {
		if row.Username != "shop_deploy" || !row.FTPAccess || !row.SFTPAccess || !row.IsEnabled {
			t.Fatalf("row wrong: %+v", row)
		}
	}
	// Host mutations ran synchronously: create + chroot flip + sshd sync.
	for _, cmd := range []string{"ftpaccount.create", "ssh.user.home_chown", "ftpaccount.sshd_sync"} {
		if mockCalled(mock, cmd) != 1 {
			t.Fatalf("expected exactly one %s call", cmd)
		}
	}
	// The response must never echo the password.
	if contains(rec.Body.String(), "longenough-pass1") {
		t.Fatal("password echoed in response")
	}
}

func TestFtpAPICreate_CapExceeded(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.rows["a1"] = &models.FtpAccount{ID: "a1", UserID: "u1", Username: "shop_x"}
	mock := ftpMockAgent()
	r := ftpTestRouter(t, repo, mock, ftpPkg(1))

	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"deploy","home_path":"/home/shop/site","password":"longenough-pass1"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	if mockCalled(mock, "ftpaccount.create") != 0 {
		t.Fatal("agent must not be called when over cap")
	}
}

func TestFtpAPICreate_NotInPackage(t *testing.T) {
	r := ftpTestRouter(t, newFakeFtpRepo(), ftpMockAgent(), ftpPkg(0))
	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"deploy","home_path":"/home/shop/site","password":"longenough-pass1"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestFtpAPICreate_Validation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"bad label", `{"label":"Bad Label","home_path":"/home/shop/site","password":"longenough-pass1"}`},
		{"home outside tenant", `{"label":"deploy","home_path":"/etc","password":"longenough-pass1"}`},
		{"home traversal", `{"label":"deploy","home_path":"/home/shop/../other","password":"longenough-pass1"}`},
		{"home with space", `{"label":"deploy","home_path":"/home/shop/my site","password":"longenough-pass1"}`},
		{"short password", `{"label":"deploy","home_path":"/home/shop/site","password":"short"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := ftpMockAgent()
			r := ftpTestRouter(t, newFakeFtpRepo(), mock, ftpPkg(3))
			rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
			}
			if mockCalled(mock, "ftpaccount.create") != 0 {
				t.Fatal("agent must not be called on validation failure")
			}
		})
	}
}

func TestFtpAPICreate_AgentFailureNoRow(t *testing.T) {
	repo := newFakeFtpRepo()
	mock := ftpMockAgent().OnError("ftpaccount.create",
		&agent.AgentError{Code: "permission_denied", Message: "nope"})
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"deploy","home_path":"/home/shop/site","password":"longenough-pass1"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 from mapped agent error, got %d", rec.Code)
	}
	if len(repo.rows) != 0 {
		t.Fatal("no row may exist after a failed host create")
	}
}

// GH #1053: the agent preflights filesystem quota for isolated accounts and
// returns failed_precondition when it isn't enabled. The API must surface that
// as an actionable 503 isolation_unavailable carrying the agent's message —
// NOT the opaque 502 "host operation failed" that hid the cause (altomarketing).
func TestMapFtpAgentError_FailedPreconditionIsActionable(t *testing.T) {
	status, payload := mapFtpAgentError(
		&agent.AgentError{Code: "failed_precondition", Message: "filesystem disk quota is not enabled on /home — ..."},
		"create_failed",
	)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", status)
	}
	if payload["error"] != "isolation_unavailable" {
		t.Fatalf("want isolation_unavailable, got %v", payload["error"])
	}
	if got, _ := payload["detail"].(string); !strings.Contains(got, "quota is not enabled") {
		t.Fatalf("agent's actionable detail must reach the client, got %q", got)
	}
}

// JAB-262: with reserve-then-create, the row is inserted BEFORE any host alias.
// A reservation-insert failure therefore happens before the agent is touched —
// there is no host alias to compensate, and neither create nor delete fires.
func TestFtpAPICreate_ReserveFailureNoHostTouch(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.createErr = repository.ErrConflict // reservation insert fails (dup)
	mock := ftpMockAgent()
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"deploy","home_path":"/home/shop/site","password":"longenough-pass1"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	if mockCalled(mock, "ftpaccount.create") != 0 {
		t.Fatal("no host alias may be created when the reservation fails")
	}
	if mockCalled(mock, "ftpaccount.delete") != 0 {
		t.Fatal("nothing to compensate — no delete should be dispatched")
	}
}

func TestFtpAPIDelete_HostFirstRowKeptOnFailure(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.rows["a1"] = &models.FtpAccount{ID: "a1", UserID: "u1", Username: "shop_deploy"}
	mock := ftpMockAgent().OnError("ftpaccount.delete",
		&agent.AgentError{Code: "internal", Message: "boom"})
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	rec := doReq(t, r, http.MethodDelete, "/me/ftp-accounts/a1", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if _, ok := repo.rows["a1"]; !ok {
		t.Fatal("row must be KEPT when the host delete fails — it is the only handle")
	}
}

func TestFtpAPIDelete_Success(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.rows["a1"] = &models.FtpAccount{ID: "a1", UserID: "u1", Username: "shop_deploy"}
	mock := ftpMockAgent()
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	rec := doReq(t, r, http.MethodDelete, "/me/ftp-accounts/a1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(repo.rows) != 0 {
		t.Fatal("row must be deleted")
	}
}

func TestFtpAPIUpdate_TogglesViaAgent(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.rows["a1"] = &models.FtpAccount{ID: "a1", UserID: "u1", Username: "shop_deploy", SFTPAccess: true, IsEnabled: true}
	mock := ftpMockAgent()
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	rec := doReq(t, r, http.MethodPatch, "/me/ftp-accounts/a1", `{"is_enabled":false,"ftp_access":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if mockCalled(mock, "ftpaccount.set_access") != 1 {
		t.Fatal("expected set_access agent call")
	}
	if repo.rows["a1"].IsEnabled || !repo.rows["a1"].FTPAccess {
		t.Fatalf("row not updated: %+v", repo.rows["a1"])
	}
}

func TestFtpAPIPassword_OwnershipAndLength(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.rows["a1"] = &models.FtpAccount{ID: "a1", UserID: "OTHER", Username: "other_x"}
	mock := ftpMockAgent()
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	// Not the caller's account → 404, never touches the agent.
	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts/a1/password", `{"password":"longenough-pass1"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for foreign account, got %d", rec.Code)
	}
	if mockCalled(mock, "ftpaccount.set_password") != 0 {
		t.Fatal("agent must not be called for a foreign account")
	}

	// Too short → 422.
	rec = doReq(t, r, http.MethodPost, "/me/ftp-accounts/a1/password", `{"password":"short"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
}

// JAB-258: after a downgrade to zero FTP cap, CREATE is blocked (403) but
// management routes — list, disable (update), delete — MUST still work so
// the tenant can revoke credentials the package no longer entitles.
func TestFtpAPI_ZeroCap_ManagementStillWorks(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.rows["a1"] = &models.FtpAccount{ID: "a1", UserID: "u1", Username: "shop_deploy", SFTPAccess: true, IsEnabled: true}
	mock := ftpMockAgent()
	r := ftpTestRouter(t, repo, mock, ftpPkg(0)) // downgraded: zero FTP

	// create blocked
	if rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"x","home_path":"/home/shop/x","password":"password1234"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("create at zero cap: got %d, want 403", rec.Code)
	}
	// list works
	if rec := doReq(t, r, http.MethodGet, "/me/ftp-accounts", ""); rec.Code != http.StatusOK {
		t.Fatalf("list at zero cap: got %d, want 200", rec.Code)
	}
	// disable works
	if rec := doReq(t, r, http.MethodPatch, "/me/ftp-accounts/a1", `{"is_enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disable at zero cap: got %d, want 200", rec.Code)
	}
	// delete works
	if rec := doReq(t, r, http.MethodDelete, "/me/ftp-accounts/a1", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete at zero cap: got %d, want 200", rec.Code)
	}
	if len(repo.rows) != 0 {
		t.Fatal("row not deleted at zero cap")
	}
}

func TestFtpAPICreate_Isolated(t *testing.T) {
	repo := newFakeFtpRepo()
	mock := ftpMockAgent()
	pkg := ftpPkg(3)
	pkg.DiskQuotaMB = 1000
	r := ftpTestRouter(t, repo, mock, pkg)

	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"printer","home_path":"/home/shop/ftp/printer","password":"longenough-pass1","isolated":true,"quota_mb":200}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var row *models.FtpAccount
	for _, x := range repo.rows {
		row = x
	}
	if row == nil || !row.Isolated || row.UID == nil || *row.UID < ftpSubaccountUIDMin ||
		row.QuotaMB != 200 || row.JailPath != "/var/lib/jabali-ftp-jails/shop/shop_printer" {
		t.Fatalf("isolated row wrong: %+v", row)
	}
	// The agent create carried the isolated fields the agent re-validates.
	var params map[string]any
	for _, c := range mock.Calls() {
		if c.Command == "ftpaccount.create" {
			_ = json.Unmarshal(c.Params, &params)
		}
	}
	if params["isolated"] != true ||
		params["jail_path"] != "/var/lib/jabali-ftp-jails/shop/shop_printer" ||
		params["quota_mount"] != "/" {
		t.Fatalf("agent create params missing isolated fields: %v", params)
	}
}

func TestFtpAPICreate_IsolatedRequiresQuota(t *testing.T) {
	repo := newFakeFtpRepo()
	r := ftpTestRouter(t, repo, ftpMockAgent(), ftpPkg(3))
	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"printer","home_path":"/home/shop/ftp/printer","password":"longenough-pass1","isolated":true}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 (quota required), got %d: %s", rec.Code, rec.Body.String())
	}
	if len(repo.rows) != 0 {
		t.Fatal("no row should be created when quota is missing")
	}
}

func TestFtpAPICreate_IsolatedQuotaSplitExceeded(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.rows["x"] = &models.FtpAccount{ID: "x", UserID: "u1", Username: "shop_a", Isolated: true, QuotaMB: 900}
	pkg := ftpPkg(3)
	pkg.DiskQuotaMB = 1000
	r := ftpTestRouter(t, repo, ftpMockAgent(), pkg)
	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"printer","home_path":"/home/shop/ftp/printer","password":"longenough-pass1","isolated":true,"quota_mb":200}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 (split exceeded 900+200>1000), got %d: %s", rec.Code, rec.Body.String())
	}
}
