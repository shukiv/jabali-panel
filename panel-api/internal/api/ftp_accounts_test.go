package api

import (
	"context"
	"net/http"
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

func ftpTestRouter(t *testing.T, repo *fakeFtpRepo, mock *agent.MockClient, pkg *models.HostingPackage) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	uname := "shop"
	pkgID := "pkg1"
	users := &usersMap{m: map[string]*models.User{
		"u1": {ID: "u1", Username: &uname, PackageID: &pkgID},
	}}
	h := &ftpAccountsHandler{cfg: FtpAccountsHandlerConfig{
		Repo:     repo,
		Users:    users,
		Packages: &fakePkgRepo{pkg: pkg},
		Agent:    mock,
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

func TestFtpAPICreate_RowFailureCompensatesHost(t *testing.T) {
	repo := newFakeFtpRepo()
	repo.createErr = repository.ErrConflict
	mock := ftpMockAgent()
	r := ftpTestRouter(t, repo, mock, ftpPkg(3))

	rec := doReq(t, r, http.MethodPost, "/me/ftp-accounts",
		`{"label":"deploy","home_path":"/home/shop/site","password":"longenough-pass1"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	// The host alias must be torn back down when the row write fails.
	if mockCalled(mock, "ftpaccount.delete") != 1 {
		t.Fatal("expected compensating ftpaccount.delete")
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
