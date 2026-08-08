package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeIPRepo is the in-test stand-in for repository.ManagedIPRepository.
// Keeps a flat slice; reads/writes are not concurrency-safe (tests are
// single-goroutine).
type fakeIPRepo struct {
	rows       []models.ManagedIP
	nextID     uint64
	createErr  error
	deleteErr  error
	countCalls int
	countByID  map[uint64]int64
}

func newFakeIPRepo() *fakeIPRepo {
	return &fakeIPRepo{nextID: 1, countByID: map[uint64]int64{}}
}

func (r *fakeIPRepo) Create(ctx context.Context, ip *models.ManagedIP) error {
	if r.createErr != nil {
		return r.createErr
	}
	for _, existing := range r.rows {
		if existing.Address == ip.Address {
			return repository.ErrConflict
		}
	}
	ip.ID = r.nextID
	r.nextID++
	r.rows = append(r.rows, *ip)
	return nil
}

func (r *fakeIPRepo) Update(ctx context.Context, ip *models.ManagedIP) error {
	for i := range r.rows {
		if r.rows[i].ID == ip.ID {
			r.rows[i] = *ip
			return nil
		}
	}
	return repository.ErrNotFound
}

func (r *fakeIPRepo) Delete(ctx context.Context, id uint64) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	for i := range r.rows {
		if r.rows[i].ID == id {
			r.rows = append(r.rows[:i], r.rows[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

func (r *fakeIPRepo) FindByID(ctx context.Context, id uint64) (*models.ManagedIP, error) {
	for i := range r.rows {
		if r.rows[i].ID == id {
			cp := r.rows[i]
			return &cp, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *fakeIPRepo) FindByAddress(ctx context.Context, addr string) (*models.ManagedIP, error) {
	for i := range r.rows {
		if r.rows[i].Address == addr {
			cp := r.rows[i]
			return &cp, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *fakeIPRepo) ListAll(ctx context.Context) ([]models.ManagedIP, error) {
	out := make([]models.ManagedIP, len(r.rows))
	copy(out, r.rows)
	return out, nil
}

func (r *fakeIPRepo) FindUnbound(ctx context.Context) ([]models.ManagedIP, error) {
	var out []models.ManagedIP
	for _, row := range r.rows {
		if !row.IsBound {
			out = append(out, row)
		}
	}
	return out, nil
}

func (r *fakeIPRepo) CountDomainsUsingIP(ctx context.Context, id uint64) (int64, error) {
	r.countCalls++
	return r.countByID[id], nil
}

func (r *fakeIPRepo) FindDefaultByFamily(ctx context.Context, family string) (*models.ManagedIP, error) {
	for i := range r.rows {
		if r.rows[i].Family == family && r.rows[i].IsDefault {
			cp := r.rows[i]
			return &cp, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *fakeIPRepo) EnsureDefault(ctx context.Context, address, family string) error {
	return nil
}

// newIPRouter builds a Gin engine wired to the IP handler. Injects
// admin claims via the same pattern server_settings_test uses, so the
// real RequireAdmin middleware passes without us having to mock auth.
func newIPRouter(t *testing.T, repo repository.ManagedIPRepository) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})

	RegisterIPRoutes(v1, IPHandlerConfig{
		Repo:    repo,
		Domains: nil,
		Log:     slog.Default(),
	})
	return r
}

// doJSON is a tiny helper to issue JSON requests in tests.
func doJSON(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Loopback so internal endpoints accept; admin middleware is bypassed
	// at the test-engine level above so this only matters for /internal/.
	req.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestValidateRoutableIP(t *testing.T) {
	cases := []struct {
		addr    string
		wantErr bool
	}{
		{"203.0.113.5", false},
		{"2001:db8::1", false},
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", true},
		{"::", true},
		{"169.254.1.1", true},
		{"224.0.0.1", true},
		{"fe80::1", true},
		{"not-an-ip", true},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			err := validateRoutableIP(tc.addr)
			if tc.wantErr && err == nil {
				t.Errorf("validateRoutableIP(%q) = nil, want error", tc.addr)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateRoutableIP(%q) = %v, want nil", tc.addr, err)
			}
		})
	}
}

func TestIPHandler_Create_HappyPath(t *testing.T) {
	repo := newFakeIPRepo()
	r := newIPRouter(t, repo)

	w := doJSON(t, r, "POST", "/api/v1/admin/ips", map[string]any{
		"address":            "203.0.113.42",
		"label":              "extra v4",
		"is_user_selectable": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got ipResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Address != "203.0.113.42" || got.Family != "ipv4" {
		t.Errorf("got %+v", got)
	}
	if !got.IsUserSelectable {
		t.Errorf("is_user_selectable not preserved")
	}
}

func TestIPHandler_Create_InvalidAddress(t *testing.T) {
	repo := newFakeIPRepo()
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "POST", "/api/v1/admin/ips", map[string]any{"address": "garbage"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestIPHandler_Create_LoopbackRejected(t *testing.T) {
	repo := newFakeIPRepo()
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "POST", "/api/v1/admin/ips", map[string]any{"address": "127.0.0.1"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "loopback") {
		t.Errorf("error doesn't mention loopback: %s", w.Body.String())
	}
}

func TestIPHandler_Create_DuplicateAddress(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{{ID: 1, Address: "203.0.113.5", Family: "ipv4"}}
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "POST", "/api/v1/admin/ips", map[string]any{"address": "203.0.113.5"})
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestIPHandler_Delete_DefaultRefused(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{{ID: 7, Address: "203.0.113.1", Family: "ipv4", IsDefault: true}}
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "DELETE", "/api/v1/admin/ips/7", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "default") {
		t.Errorf("error doesn't mention default: %s", w.Body.String())
	}
}

func TestIPHandler_Delete_InUseRefused(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{{ID: 9, Address: "203.0.113.9", Family: "ipv4"}}
	repo.countByID[9] = 3
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "DELETE", "/api/v1/admin/ips/9", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ip_in_use") {
		t.Errorf("expected ip_in_use error: %s", w.Body.String())
	}
}

func TestIPHandler_Delete_HappyPath(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{{ID: 5, Address: "203.0.113.5", Family: "ipv4"}}
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "DELETE", "/api/v1/admin/ips/5", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(repo.rows) != 0 {
		t.Errorf("row not deleted")
	}
}

func TestIPHandler_Update_PromoteDefault(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{
		{ID: 1, Address: "203.0.113.1", Family: "ipv4", IsDefault: true},
		{ID: 2, Address: "203.0.113.2", Family: "ipv4", IsDefault: false},
	}
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "PATCH", "/api/v1/admin/ips/2", map[string]any{"is_default": true})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// Old default demoted, new default promoted.
	if repo.rows[0].IsDefault {
		t.Errorf("old default was not demoted")
	}
	if !repo.rows[1].IsDefault {
		t.Errorf("new default was not promoted")
	}
}

func TestIPHandler_Update_DemoteRefused(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{{ID: 1, Address: "203.0.113.1", Family: "ipv4", IsDefault: true}}
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "PATCH", "/api/v1/admin/ips/1", map[string]any{"is_default": false})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestIPHandler_List(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{
		{ID: 1, Address: "203.0.113.1", Family: "ipv4", IsDefault: true},
		{ID: 2, Address: "2001:db8::1", Family: "ipv6", IsDefault: true},
	}
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "GET", "/api/v1/admin/ips", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp ipListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 || len(resp.Data) != 2 {
		t.Errorf("got %d rows, want 2", resp.Total)
	}
}

func TestIPHandler_List_FamilyFilter(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{
		{ID: 1, Address: "203.0.113.1", Family: "ipv4"},
		{ID: 2, Address: "2001:db8::1", Family: "ipv6"},
	}
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "GET", "/api/v1/admin/ips?family=ipv6", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp ipListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Data[0].Family != "ipv6" {
		t.Errorf("filter didn't apply: %+v", resp)
	}
}

func TestIPHandler_UserList_FiltersUnselectable(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{
		{ID: 1, Address: "203.0.113.1", Family: "ipv4", IsUserSelectable: false},
		{ID: 2, Address: "203.0.113.2", Family: "ipv4", IsUserSelectable: true},
	}
	r := newIPRouter(t, repo)
	w := doJSON(t, r, "GET", "/api/v1/user/ips", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp ipListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Data[0].ID != 2 {
		t.Errorf("user list returned non-user-selectable rows: %+v", resp)
	}
}

func TestParseIPID(t *testing.T) {
	cases := []struct {
		in      string
		want    uint64
		wantErr bool
	}{
		{"42", 42, false},
		{"0", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseIPID(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("parseIPID(%q) = %d nil, want error", tc.in, got)
			}
			if !tc.wantErr && (err != nil || got != tc.want) {
				t.Errorf("parseIPID(%q) = %d %v, want %d nil", tc.in, got, err, tc.want)
			}
		})
	}
}

// TestRequireLocalhost_Reject pins the new middleware so a refactor
// can't silently open the /internal/agent/ surface to non-loopback
// callers.
func TestRequireLocalhost_Reject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/internal", middleware.RequireLocalhost(), func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	req := httptest.NewRequest("GET", "/internal", nil)
	req.RemoteAddr = "8.8.8.8:5555"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-loopback got %d, want 403", w.Code)
	}
}

func TestRequireLocalhost_Accept(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/internal", middleware.RequireLocalhost(), func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	req := httptest.NewRequest("GET", "/internal", nil)
	// Unix-socket peer ("@" is what net/http sets), which is the production
	// transport for internal routes. Loopback TCP is deliberately no longer
	// accepted by default — on a multi-tenant box any tenant process can
	// bind 127.0.0.1, so it is not a trust boundary.
	req.RemoteAddr = "@"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("unix-socket peer got %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// silence unused-import warnings if any lint pass complains.
var _ = errors.New
var _ = strconv.Itoa
