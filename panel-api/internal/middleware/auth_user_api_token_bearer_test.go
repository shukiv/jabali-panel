package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// GH #2245: a Server API Access (automation) token sent as a Bearer to
// /api/v1 got "Kratos session cookie not found", which says nothing about
// the token. It now gets a 401 that names the two token kinds. A request
// with a session cookie still goes to the cookie path.
func runUserAuth(t *testing.T, authz string, cookie bool) (int, map[string]string, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	kratosRan := false
	kratos := func(c *gin.Context) {
		kratosRan = true
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing_session"})
	}
	r := gin.New()
	r.GET("/api/v1/x", RequireUserAuth(nil, nil, kratos), func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	if cookie {
		req.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "s"})
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body, kratosRan
}

func TestRequireUserAuth_OtherBearerNamesTheTokenKinds(t *testing.T) {
	code, body, kratosRan := runUserAuth(t, "Bearer 3f9a0c1d2e4b5a6978c0d1e2f3a4b5c6", false)
	if code != http.StatusUnauthorized || body["error"] != "invalid_token" {
		t.Fatalf("code=%d body=%v, want 401 invalid_token", code, body)
	}
	if !strings.Contains(body["message"], "jat_") || !strings.Contains(body["message"], "Jabali-HMAC") {
		t.Errorf("message should name both token kinds: %q", body["message"])
	}
	if kratosRan {
		t.Error("the cookie path ran for a request with no cookie")
	}
}

func TestRequireUserAuth_OtherBearerWithCookieKeepsTheCookiePath(t *testing.T) {
	_, _, kratosRan := runUserAuth(t, "Bearer 3f9a0c1d2e4b5a6978c0d1e2f3a4b5c6", true)
	if !kratosRan {
		t.Fatal("a request with a session cookie must still go to the cookie path")
	}
}

func TestRequireUserAuth_NoAuthorizationKeepsTheCookiePath(t *testing.T) {
	_, body, kratosRan := runUserAuth(t, "", false)
	if !kratosRan || body["error"] != "missing_session" {
		t.Fatalf("kratosRan=%v body=%v, want the cookie path's missing_session", kratosRan, body)
	}
}
