package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// JAB-235 admin token endpoints. Pinned properties: verify-before-store, the
// token lands SEALED (not plaintext) and never appears in any response, a
// missing sso.key refuses storage, and clear actually clears.

func testSSOKey(t *testing.T) *ssokey.Key {
	t.Helper()
	var k ssokey.Key
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatal(err)
	}
	return &k
}

func cfTokenRouter(t *testing.T, repo *mockServerSettingsRepo, key *ssokey.Key, verify func(context.Context, string) (int, error)) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true}) })
	RegisterServerSettingsRoutes(r.Group("/api/v1"), ServerSettingsHandlerConfig{
		Repo:     repo,
		SSOKey:   key,
		CFVerify: verify,
	})
	return r
}

func TestCFToken_SetVerifiesAndStoresSealed(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{}}
	key := testSSOKey(t)
	var verifiedWith string
	router := cfTokenRouter(t, repo, key, func(_ context.Context, tok string) (int, error) {
		verifiedWith = tok
		return 42, nil
	})

	body := bytes.NewBufferString(`{"token":"  cf-secret-token  "}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/cloudflare-token", body)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if verifiedWith != "cf-secret-token" {
		t.Errorf("verified with %q, want the trimmed token", verifiedWith)
	}
	var resp cfTokenSetResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Configured || resp.Zones != 42 {
		t.Errorf("response %+v, want configured with 42 zones", resp)
	}
	if strings.Contains(w.Body.String(), "cf-secret-token") {
		t.Fatal("response echoed the token")
	}
	stored := repo.getResult.CFAPITokenEnc
	if len(stored) == 0 {
		t.Fatal("token not stored")
	}
	if bytes.Contains(stored, []byte("cf-secret-token")) {
		t.Fatal("token stored in PLAINTEXT — must be sealed")
	}
	plain, err := key.Open(stored)
	if err != nil || string(plain) != "cf-secret-token" {
		t.Fatalf("stored envelope does not unseal to the token: %q, %v", plain, err)
	}
}

func TestCFToken_BadTokenNotStored(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{}}
	router := cfTokenRouter(t, repo, testSSOKey(t), func(context.Context, string) (int, error) {
		return 0, errors.New("cloudflare rejected the token")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/cloudflare-token",
		bytes.NewBufferString(`{"token":"wrong"}`))
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", w.Code)
	}
	if len(repo.getResult.CFAPITokenEnc) != 0 {
		t.Fatal("a token that failed verification was stored anyway")
	}
}

func TestCFToken_NoSSOKeyRefuses(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{}}
	router := cfTokenRouter(t, repo, nil, func(context.Context, string) (int, error) { return 1, nil })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/cloudflare-token",
		bytes.NewBufferString(`{"token":"tok"}`))
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 — plaintext-at-rest is not an option for this secret", w.Code)
	}
	if len(repo.getResult.CFAPITokenEnc) != 0 {
		t.Fatal("token stored without a sealing key")
	}
}

func TestCFToken_StatusAndClear(t *testing.T) {
	key := testSSOKey(t)
	sealed, _ := key.Seal([]byte("tok"))
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{CFAPITokenEnc: sealed}}
	router := cfTokenRouter(t, repo, key, nil)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/cloudflare-token", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"configured":true`) {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/settings/cloudflare-token", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("clear status %d", w.Code)
	}
	if len(repo.getResult.CFAPITokenEnc) != 0 {
		t.Fatal("clear did not remove the sealed token")
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/cloudflare-token", nil))
	if !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatalf("status after clear: %s", w.Body.String())
	}
}
