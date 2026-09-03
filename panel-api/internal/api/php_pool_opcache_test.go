package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// statefulOverrideRepo is an in-memory PHPPoolIniOverride store so the opcache
// mapping can be verified via readback (the shared no-op mock can't).
type statefulOverrideRepo struct {
	rows map[string]*models.PHPPoolIniOverride // id -> row
	seq  int
}

func newStatefulOverrideRepo() *statefulOverrideRepo {
	return &statefulOverrideRepo{rows: map[string]*models.PHPPoolIniOverride{}}
}
func (r *statefulOverrideRepo) Create(_ context.Context, o *models.PHPPoolIniOverride) error {
	r.seq++
	cp := *o
	r.rows[o.ID] = &cp
	return nil
}
func (r *statefulOverrideRepo) FindByID(_ context.Context, id string) (*models.PHPPoolIniOverride, error) {
	return r.rows[id], nil
}
func (r *statefulOverrideRepo) ListByPool(_ context.Context, poolID string) ([]models.PHPPoolIniOverride, error) {
	var out []models.PHPPoolIniOverride
	for _, v := range r.rows {
		if v.PoolID == poolID {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (r *statefulOverrideRepo) Update(_ context.Context, o *models.PHPPoolIniOverride) error {
	if _, ok := r.rows[o.ID]; ok {
		cp := *o
		r.rows[o.ID] = &cp
	}
	return nil
}
func (r *statefulOverrideRepo) Delete(_ context.Context, id string) error {
	delete(r.rows, id)
	return nil
}
func (r *statefulOverrideRepo) byDirective(poolID, dir string) (models.PHPPoolIniOverride, bool) {
	for _, v := range r.rows {
		if v.PoolID == poolID && v.Directive == dir {
			return *v, true
		}
	}
	return models.PHPPoolIniOverride{}, false
}

func setupOpcacheRouter(t *testing.T, ov *statefulOverrideRepo, calls *[]string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"}); c.Next() })
	pkgID := "pkg1"
	uname := "alice"
	users := &mockUserRepo{users: map[string]*models.User{"u1": {ID: "u1", PackageID: &pkgID, Username: &uname}}}
	packages := &mockPackageRepo{packages: map[string]*models.HostingPackage{"pkg1": {ID: "pkg1", FpmUserCanEdit: true}}}
	pr := newMockPHPPoolRepo()
	pr.Create(context.Background(), &models.PHPPool{ID: "p1", UserID: "u1", PHPVersion: "8.4"})
	RegisterPHPUserTuningRoutes(r.Group("/api/v1"), PHPUserTuningHandlerConfig{
		Agent: &mockAgent{callFn: func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
			if calls != nil {
				*calls = append(*calls, cmd)
			}
			if cmd == "php.version.list" {
				return json.RawMessage(`{"versions":["8.4"]}`), nil
			}
			return json.RawMessage(`{}`), nil
		}},
		Users: users, Packages: packages, PHPPools: pr,
		PHPPoolIniOverrides: ov, Modes: mockModesRepo{},
	})
	return r
}

func putOpcache(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/me/php-opcache", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestOpcache_SetMapsDirectivesAndRestarts(t *testing.T) {
	ov := newStatefulOverrideRepo()
	var calls []string
	r := setupOpcacheRouter(t, ov, &calls)

	w := putOpcache(t, r, `{"php_version":"8.4","enable":false,"jit_enabled":true,"jit_buffer_size_mb":64,"memory_consumption_mb":128}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	// Directive rows written correctly.
	if o, ok := ov.byDirective("p1", dirOpcacheEnable); !ok || o.Kind != "flag" || o.Value != "off" {
		t.Errorf("opcache.enable: %+v ok=%v", o, ok)
	}
	if o, ok := ov.byDirective("p1", dirOpcacheJit); !ok || o.Value != "tracing" {
		t.Errorf("opcache.jit: %+v ok=%v", o, ok)
	}
	if o, ok := ov.byDirective("p1", dirOpcacheJitBufferSize); !ok || o.Value != "64M" {
		t.Errorf("opcache.jit_buffer_size: %+v ok=%v", o, ok)
	}
	if o, ok := ov.byDirective("p1", dirOpcacheMemory); !ok || o.Value != "128" {
		t.Errorf("opcache.memory_consumption: %+v ok=%v", o, ok)
	}
	// The master restart (php.opcache.reset) must be chained after the apply so
	// OPcache/JIT changes take effect (reload keeps the SHM).
	var sawApply, sawReset bool
	for _, c := range calls {
		if c == "php.pool.apply" {
			sawApply = true
		}
		if c == "php.opcache.reset" {
			sawReset = true
		}
	}
	if !sawApply || !sawReset {
		t.Errorf("expected php.pool.apply AND php.opcache.reset, got %v", calls)
	}
}

func TestOpcache_GetReadsBack(t *testing.T) {
	ov := newStatefulOverrideRepo()
	r := setupOpcacheRouter(t, ov, nil)
	if w := putOpcache(t, r, `{"php_version":"8.4","enable":true,"jit_enabled":true,"jit_buffer_size_mb":32}`); w.Code != http.StatusOK {
		t.Fatalf("set failed: %d %s", w.Code, w.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/php-opcache?php_version=8.4", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var got opcacheSettings
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Enable == nil || !*got.Enable {
		t.Errorf("enable readback: %+v", got.Enable)
	}
	if got.JitEnabled == nil || !*got.JitEnabled {
		t.Errorf("jit_enabled readback: %+v", got.JitEnabled)
	}
	if got.JitBufferSizeMB == nil || *got.JitBufferSizeMB != 32 {
		t.Errorf("jit_buffer readback: %+v", got.JitBufferSizeMB)
	}
}

func TestOpcache_ClampsRejectOutOfRange(t *testing.T) {
	r := setupOpcacheRouter(t, newStatefulOverrideRepo(), nil)
	for _, body := range []string{
		`{"php_version":"8.4","memory_consumption_mb":5000}`,
		`{"php_version":"8.4","jit_buffer_size_mb":9999}`,
		`{"php_version":"8.4","revalidate_freq":99999}`,
	} {
		if w := putOpcache(t, r, body); w.Code != http.StatusBadRequest {
			t.Errorf("want 400 for %s, got %d", body, w.Code)
		}
	}
}

func TestOpcache_UnsetFieldDeletesOverride(t *testing.T) {
	ov := newStatefulOverrideRepo()
	r := setupOpcacheRouter(t, ov, nil)
	if w := putOpcache(t, r, `{"php_version":"8.4","enable":false}`); w.Code != http.StatusOK {
		t.Fatalf("set: %d", w.Code)
	}
	if _, ok := ov.byDirective("p1", dirOpcacheEnable); !ok {
		t.Fatal("expected opcache.enable row after set")
	}
	// A PUT without enable clears it back to server default.
	if w := putOpcache(t, r, `{"php_version":"8.4"}`); w.Code != http.StatusOK {
		t.Fatalf("clear: %d", w.Code)
	}
	if _, ok := ov.byDirective("p1", dirOpcacheEnable); ok {
		t.Error("opcache.enable override must be deleted when field omitted")
	}
}
