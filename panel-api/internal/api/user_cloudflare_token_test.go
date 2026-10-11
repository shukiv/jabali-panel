package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/hostedsvc"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// GH #2032 — a tenant's own Cloudflare token: Verify & Save proves it can
// write in the zones holding the tenant's domains before storing it.

const goodCFToken = "cf-token-abc_DEF-123"

type memUserCFTokens struct {
	rows    map[string]*models.UserCloudflareToken
	deleted []string
}

func (m *memUserCFTokens) Get(_ context.Context, uid string) (*models.UserCloudflareToken, error) {
	if row, ok := m.rows[uid]; ok {
		return row, nil
	}
	return nil, repository.ErrNotFound
}

func (m *memUserCFTokens) Put(_ context.Context, row *models.UserCloudflareToken) error {
	m.rows[row.UserID] = row
	return nil
}

func (m *memUserCFTokens) Delete(_ context.Context, uid string) error {
	m.deleted = append(m.deleted, uid)
	if _, ok := m.rows[uid]; !ok {
		return repository.ErrNotFound
	}
	delete(m.rows, uid)
	return nil
}

type fakeUserDomains struct {
	byUser map[string][]string
	asked  []string
}

func (f *fakeUserDomains) ListByUserID(_ context.Context, uid string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	f.asked = append(f.asked, uid)
	var out []models.Domain
	for _, n := range f.byUser[uid] {
		out = append(out, models.Domain{Name: n, UserID: uid})
	}
	return out, int64(len(out)), nil
}

type fakeCFProbe struct {
	token     string
	verifyErr error
	zones     []hostedsvc.CloudflareZone
	listErr   error
	setErr    map[string]error // zone id → error
	clearErr  map[string]error
	sets      []string // "zoneID name"
	clears    []string
}

func (f *fakeCFProbe) VerifyToken(context.Context) (int, error) { return len(f.zones), f.verifyErr }
func (f *fakeCFProbe) ListZones(context.Context) ([]hostedsvc.CloudflareZone, error) {
	return f.zones, f.listErr
}
func (f *fakeCFProbe) SetChallengeTXT(_ context.Context, zoneID, name, value string) error {
	if value == "" {
		return errors.New("empty value")
	}
	f.sets = append(f.sets, zoneID+" "+name)
	return f.setErr[zoneID]
}
func (f *fakeCFProbe) ClearChallengeTXT(ctx context.Context, zoneID, name string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f.clears = append(f.clears, zoneID+" "+name)
	return f.clearErr[zoneID]
}

type cfTokenFixture struct {
	tokens  *memUserCFTokens
	domains *fakeUserDomains
	probe   *fakeCFProbe
	key     *ssokey.Key
	clients int
	cfg     UserCloudflareTokenHandlerConfig
}

func newCFTokenFixture() *cfTokenFixture {
	f := &cfTokenFixture{
		tokens:  &memUserCFTokens{rows: map[string]*models.UserCloudflareToken{}},
		domains: &fakeUserDomains{byUser: map[string][]string{"u1": {"example.com", "shop.example.com", "other.org"}}},
		probe: &fakeCFProbe{zones: []hostedsvc.CloudflareZone{
			{ID: "z1", Name: "example.com"},
			{ID: "z2", Name: "other.org"},
			{ID: "z3", Name: "someone-else.net"},
		}},
		key: &ssokey.Key{},
	}
	f.cfg = UserCloudflareTokenHandlerConfig{
		Tokens:  f.tokens,
		Domains: f.domains,
		SSOKey:  f.key,
		NewClient: func(token string) CloudflareProbeClient {
			f.clients++
			f.probe.token = token
			return f.probe
		},
	}
	return f
}

func (f *cfTokenFixture) do(t *testing.T, method, uid, body string) (int, map[string]any, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if uid != "" {
			ginctx.SetClaims(c, &auth.AccessClaims{UserID: uid})
		}
	})
	RegisterUserCloudflareTokenRoutes(r.Group("/api/v1"), f.cfg)
	req := httptest.NewRequest(method, "/api/v1/me/cloudflare-token", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func putBody(tok string) string {
	b, _ := json.Marshal(map[string]string{"token": tok})
	return string(b)
}

func TestUserCFToken_SaveProbesTheCallersZonesThenStores(t *testing.T) {
	f := newCFTokenFixture()
	code, out, raw := f.do(t, http.MethodPut, "u1", putBody(goodCFToken))
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, raw)
	}
	if strings.Contains(raw, goodCFToken) {
		t.Fatal("response echoes the token")
	}
	if f.probe.token != goodCFToken {
		t.Fatalf("probe used token %q", f.probe.token)
	}
	if len(f.domains.asked) != 1 || f.domains.asked[0] != "u1" {
		t.Fatalf("listed domains of %v, want the caller only", f.domains.asked)
	}
	wantWrites := []string{"z1 _jabali-dns01-check.example.com", "z2 _jabali-dns01-check.other.org"}
	if fmt.Sprint(f.probe.sets) != fmt.Sprint(wantWrites) {
		t.Fatalf("probe writes = %v, want %v (never a zone without the caller's domains)", f.probe.sets, wantWrites)
	}
	if fmt.Sprint(f.probe.clears) != fmt.Sprint(wantWrites) {
		t.Fatalf("probe clears = %v, want %v", f.probe.clears, wantWrites)
	}
	row := f.tokens.rows["u1"]
	if row == nil || row.VerifiedAt.IsZero() {
		t.Fatalf("stored row = %+v", row)
	}
	if bytes.Contains(row.TokenEnc, []byte(goodCFToken)) {
		t.Fatal("token stored in the clear")
	}
	if plain, err := f.key.Open(row.TokenEnc); err != nil || string(plain) != goodCFToken {
		t.Fatalf("stored token unseals to %q, %v", plain, err)
	}
	if out["configured"] != true || fmt.Sprint(out["zones"]) != "[example.com other.org]" {
		t.Fatalf("response = %v", out)
	}
}

func TestUserCFToken_RejectsBeforeStoring(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		setup  func(f *cfTokenFixture)
		status int
		errKey string
		probed bool
	}{
		{"not JSON", "token=x", nil, http.StatusBadRequest, "invalid_request", false},
		{"empty", putBody("  "), nil, http.StatusBadRequest, "invalid_token", false},
		{"too long", putBody(strings.Repeat("a", 257)), nil, http.StatusBadRequest, "invalid_token", false},
		{"header injection", putBody("abc\r\nX-Evil: 1"), nil, http.StatusBadRequest, "invalid_token", false},
		{"space inside", putBody("abc def"), nil, http.StatusBadRequest, "invalid_token", false},
		{"no sso key", putBody(goodCFToken), func(f *cfTokenFixture) { f.cfg.SSOKey = nil }, http.StatusServiceUnavailable, "sso_key_unavailable", false},
		{"cloudflare rejects", putBody(goodCFToken), func(f *cfTokenFixture) {
			f.probe.verifyErr = errors.New("cloudflare says the token is expired, not active")
		}, http.StatusUnprocessableEntity, "token_verify_failed", false},
		{"no domains", putBody(goodCFToken), func(f *cfTokenFixture) { f.domains.byUser = nil }, http.StatusUnprocessableEntity, "no_domains", false},
		{"zone list fails", putBody(goodCFToken), func(f *cfTokenFixture) { f.probe.listErr = errors.New("cf zone list: boom") }, http.StatusUnprocessableEntity, "zone_list_failed", false},
		{"token sees none of the caller's zones", putBody(goodCFToken), func(f *cfTokenFixture) {
			f.probe.zones = []hostedsvc.CloudflareZone{{ID: "z3", Name: "someone-else.net"}, {ID: "z4", Name: "ample.com"}}
		}, http.StatusUnprocessableEntity, "no_zone_for_domains", false},
		{"cannot write", putBody(goodCFToken), func(f *cfTokenFixture) { f.probe.setErr = map[string]error{"z2": errors.New("Authentication error")} }, http.StatusUnprocessableEntity, "token_cannot_write", true},
		{"cannot delete", putBody(goodCFToken), func(f *cfTokenFixture) { f.probe.clearErr = map[string]error{"z1": errors.New("Authentication error")} }, http.StatusUnprocessableEntity, "token_cannot_delete", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newCFTokenFixture()
			if c.setup != nil {
				c.setup(f)
			}
			code, out, raw := f.do(t, http.MethodPut, "u1", c.body)
			if code != c.status || out["error"] != c.errKey {
				t.Fatalf("got %d %s, want %d %s", code, raw, c.status, c.errKey)
			}
			if len(f.tokens.rows) != 0 {
				t.Fatal("token stored although the check failed")
			}
			if probed := len(f.probe.sets) > 0; probed != c.probed {
				t.Fatalf("probe writes %v, want probed=%v", f.probe.sets, c.probed)
			}
			if strings.Contains(raw, goodCFToken) {
				t.Fatal("response echoes the token")
			}
		})
	}
}

// A failed write names the zone, and every record already written is
// still removed.
func TestUserCFToken_WriteFailureCleansUp(t *testing.T) {
	f := newCFTokenFixture()
	f.probe.setErr = map[string]error{"z2": errors.New("Authentication error")}
	_, out, _ := f.do(t, http.MethodPut, "u1", putBody(goodCFToken))
	if !strings.Contains(fmt.Sprint(out["detail"]), "other.org") {
		t.Fatalf("detail %q does not name the zone", out["detail"])
	}
	if fmt.Sprint(f.probe.clears) != "[z1 _jabali-dns01-check.example.com z2 _jabali-dns01-check.other.org]" {
		t.Fatalf("clears = %v", f.probe.clears)
	}
}

func TestUserCFToken_ProbeCap(t *testing.T) {
	f := newCFTokenFixture()
	var names []string
	f.probe.zones = nil
	for i := 0; i < cfProbeMaxZones+2; i++ {
		n := fmt.Sprintf("d%02d.com", i)
		names = append(names, n)
		f.probe.zones = append(f.probe.zones, hostedsvc.CloudflareZone{ID: fmt.Sprintf("z%02d", i), Name: n})
	}
	f.domains.byUser["u1"] = names
	code, out, raw := f.do(t, http.MethodPut, "u1", putBody(goodCFToken))
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, raw)
	}
	if len(f.probe.sets) != cfProbeMaxZones {
		t.Fatalf("probed %d zones, cap is %d", len(f.probe.sets), cfProbeMaxZones)
	}
	if out["zones_not_checked"] != float64(2) {
		t.Fatalf("zones_not_checked = %v", out["zones_not_checked"])
	}
}

func TestUserCFToken_StatusAndClear(t *testing.T) {
	f := newCFTokenFixture()
	code, out, _ := f.do(t, http.MethodGet, "u1", "")
	if code != http.StatusOK || out["configured"] != false {
		t.Fatalf("GET with none = %d %v", code, out)
	}

	sealed, _ := f.key.Seal([]byte(goodCFToken))
	f.tokens.rows["u1"] = &models.UserCloudflareToken{UserID: "u1", TokenEnc: sealed}
	f.tokens.rows["u2"] = &models.UserCloudflareToken{UserID: "u2", TokenEnc: sealed}
	code, out, raw := f.do(t, http.MethodGet, "u1", "")
	if code != http.StatusOK || out["configured"] != true || strings.Contains(raw, goodCFToken) {
		t.Fatalf("GET with one = %d %s", code, raw)
	}

	code, out, _ = f.do(t, http.MethodDelete, "u1", "")
	if code != http.StatusOK || out["configured"] != false {
		t.Fatalf("DELETE = %d %v", code, out)
	}
	if _, ok := f.tokens.rows["u2"]; !ok || fmt.Sprint(f.tokens.deleted) != "[u1]" {
		t.Fatalf("DELETE touched %v; u2 left: %v", f.tokens.deleted, ok)
	}
	code, _, _ = f.do(t, http.MethodDelete, "u1", "")
	if code != http.StatusOK {
		t.Fatalf("DELETE with none = %d, want 200", code)
	}
}

func TestUserCFToken_NeedsAUser(t *testing.T) {
	f := newCFTokenFixture()
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if code, _, _ := f.do(t, m, "", putBody(goodCFToken)); code != http.StatusUnauthorized {
			t.Errorf("%s without claims = %d, want 401", m, code)
		}
	}
	if f.clients != 0 {
		t.Fatal("a Cloudflare client was made without a user")
	}
}
