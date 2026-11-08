package middleware_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

// The /.ory login and recovery POSTs also spend a budget kept per account
// they name, so guessing one account's password from many addresses is held
// to what one address may try.

const (
	kratosLogin    = "/.ory/self-service/login?flow=x"
	kratosRecovery = "/.ory/self-service/recovery?flow=y"
	jsonType       = "application/json"
)

type kratosPost struct {
	path, ip, contentType, body string
}

// serveKratos runs one POST through mw and returns the response and the body
// the next handler read.
func serveKratos(mw gin.HandlerFunc, p kratosPost) (*httptest.ResponseRecorder, string) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var got string
	r.Any("/*any", mw, func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		got = string(b)
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, p.path, strings.NewReader(p.body))
	if p.contentType != "" {
		req.Header.Set("Content-Type", p.contentType)
	}
	req.RemoteAddr = p.ip + ":54321"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w, got
}

// kratosLimiter allows burst POSTs per address and per account, no refill.
func kratosLimiter(burst int) *middleware.RateLimiter {
	return middleware.NewRateLimiter(middleware.RateLimiterConfig{
		DefaultRate: 1000, DefaultBurst: 1000,
		KratosRate: 0, KratosBurst: burst,
	})
}

func kratosBody(fields map[string]string) string {
	b, _ := json.Marshal(fields)
	return string(b)
}

func passwordLogin(identifier string) string {
	return kratosBody(map[string]string{"method": "password", "identifier": identifier, "password": "wrong"})
}

func TestKratosFlows_AccountBudgetHoldsAcrossAddresses(t *testing.T) {
	mw := kratosLimiter(3).KratosFlows(nil)
	for _, ip := range []string{"10.0.1.1", "10.0.1.2", "10.0.1.3"} {
		if w, _ := serveKratos(mw, kratosPost{kratosLogin, ip, jsonType, passwordLogin("alice")}); w.Code != http.StatusOK {
			t.Fatalf("login for alice from %s = %d, want 200 within the account's budget", ip, w.Code)
		}
	}
	w, _ := serveKratos(mw, kratosPost{kratosLogin, "10.0.1.4", jsonType, passwordLogin("alice")})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("4th login for alice, from a 4th address = %d, want 429", w.Code)
	}
	// The same answer as the per-address limit: nothing tells a known
	// account from an unknown name.
	if w.Header().Get("Retry-After") != "60" || strings.TrimSpace(w.Body.String()) != `{"error":"rate_limited"}` {
		t.Errorf("429 = Retry-After %q body %q, want 60 and rate_limited", w.Header().Get("Retry-After"), w.Body.String())
	}
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, "10.0.1.4", jsonType, passwordLogin("bob")}); w.Code != http.StatusOK {
		t.Errorf("login for bob = %d, want 200: each account has its own budget", w.Code)
	}
}

func TestKratosFlows_AccountNameIsNormalised(t *testing.T) {
	mw := kratosLimiter(3).KratosFlows(nil)
	for i, name := range []string{"Alice@Example.com", " alice@example.com", "ALICE@EXAMPLE.COM\t"} {
		if w, _ := serveKratos(mw, kratosPost{kratosLogin, "10.0.2." + string(rune('1'+i)), jsonType, passwordLogin(name)}); w.Code != http.StatusOK {
			t.Fatalf("login %q = %d, want 200", name, w.Code)
		}
	}
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, "10.0.2.9", jsonType, passwordLogin("alice@example.com")}); w.Code != http.StatusTooManyRequests {
		t.Errorf("4th login for the same account, spelled another way = %d, want 429", w.Code)
	}
}

// The second-factor steps and the recovery code step name no account: only
// the per-address limit applies to them.
func TestKratosFlows_StepsThatNameNoAccountAreNotAccountLimited(t *testing.T) {
	mw := kratosLimiter(2).KratosFlows(nil)
	serveKratos(mw, kratosPost{kratosLogin, "10.0.3.1", jsonType, passwordLogin("alice")})
	serveKratos(mw, kratosPost{kratosLogin, "10.0.3.2", jsonType, passwordLogin("alice")})
	steps := []kratosPost{
		{kratosLogin, "10.0.3.3", jsonType, kratosBody(map[string]string{"method": "totp", "totp_code": "123456"})},
		{kratosLogin, "10.0.3.4", jsonType, kratosBody(map[string]string{"method": "lookup_secret", "lookup_secret": "abcd1234"})},
		{kratosLogin, "10.0.3.5", jsonType, kratosBody(map[string]string{"method": "passkey", "passkey_login": "{}"})},
		{kratosRecovery, "10.0.3.6", jsonType, kratosBody(map[string]string{"method": "code", "code": "12345678"})},
		{kratosLogin, "10.0.3.7", "", ""},
	}
	for _, p := range steps {
		if w, _ := serveKratos(mw, p); w.Code != http.StatusOK {
			t.Errorf("%s %s = %d, want 200", p.path, p.body, w.Code)
		}
	}
}

// Recovery is kept per email, apart from login: guessing an account's
// password doesn't stop its recovery, and the reverse.
func TestKratosFlows_RecoveryHasItsOwnAccountBudget(t *testing.T) {
	mw := kratosLimiter(2).KratosFlows(nil)
	recoverFrom := func(ip string) int {
		w, _ := serveKratos(mw, kratosPost{kratosRecovery, ip, jsonType, kratosBody(map[string]string{"method": "code", "email": "Alice@example.com"})})
		return w.Code
	}
	serveKratos(mw, kratosPost{kratosLogin, "10.0.4.1", jsonType, passwordLogin("alice@example.com")})
	serveKratos(mw, kratosPost{kratosLogin, "10.0.4.2", jsonType, passwordLogin("alice@example.com")})
	if recoverFrom("10.0.4.3") != http.StatusOK || recoverFrom("10.0.4.4") != http.StatusOK {
		t.Fatal("recovery for alice should have its own budget")
	}
	if code := recoverFrom("10.0.4.5"); code != http.StatusTooManyRequests {
		t.Errorf("3rd recovery for alice from a 3rd address = %d, want 429", code)
	}
}

func TestKratosFlows_PassesTheBodyOnUnchanged(t *testing.T) {
	mw := kratosLimiter(5).KratosFlows(nil)
	body := passwordLogin("alice")
	w, got := serveKratos(mw, kratosPost{kratosLogin, "10.0.5.1", "application/json; charset=utf-8", body})
	if w.Code != http.StatusOK || got != body {
		t.Errorf("code %d, next handler read %q, want 200 and %q", w.Code, got, body)
	}
}

// A body the limiter can't read for the account is refused, not passed on
// without the account budget: Kratos must not see a name the limiter didn't.
func TestKratosFlows_RefusesBodiesItCannotReadForTheAccount(t *testing.T) {
	mw := kratosLimiter(100).KratosFlows(nil)
	for name, c := range map[string]struct {
		p    kratosPost
		want int
	}{
		"multipart": {kratosPost{kratosLogin, "10.0.6.1", "multipart/form-data; boundary=b",
			"--b\r\nContent-Disposition: form-data; name=\"identifier\"\r\n\r\nalice\r\n--b--\r\n"}, http.StatusUnsupportedMediaType},
		"form":          {kratosPost{kratosLogin, "10.0.6.2", "application/x-www-form-urlencoded", "identifier=alice&password=x"}, http.StatusUnsupportedMediaType},
		"octet-stream":  {kratosPost{kratosRecovery, "10.0.6.3", "application/octet-stream", `{"email":"alice"}`}, http.StatusUnsupportedMediaType},
		"no type":       {kratosPost{kratosLogin, "10.0.6.4", "", passwordLogin("alice")}, http.StatusUnsupportedMediaType},
		"malformed":     {kratosPost{kratosLogin, "10.0.6.5", jsonType, `{"identifier":"alice",`}, http.StatusBadRequest},
		"not an object": {kratosPost{kratosLogin, "10.0.6.6", jsonType, `["alice"]`}, http.StatusBadRequest},
		"trailing data": {kratosPost{kratosLogin, "10.0.6.7", jsonType, passwordLogin("alice") + `{"identifier":"bob"}`}, http.StatusBadRequest},
		"two names":     {kratosPost{kratosLogin, "10.0.6.8", jsonType, `{"identifier":"decoy","Identifier":"alice","password":"x"}`}, http.StatusBadRequest},
		"two emails":    {kratosPost{kratosRecovery, "10.0.6.9", jsonType, `{"email":"decoy","email":"alice"}`}, http.StatusBadRequest},
	} {
		if w, _ := serveKratos(mw, c.p); w.Code != c.want {
			t.Errorf("%s: %d, want %d", name, w.Code, c.want)
		}
	}
	// The same name twice is one name.
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, "10.0.6.10", jsonType, `{"identifier":"alice","identifier":"Alice ","password":"x"}`}); w.Code != http.StatusOK {
		t.Errorf("the same name twice = %d, want 200", w.Code)
	}
}

// The account limit logs its own marker. The per-address marker bans the
// address through CrowdSec; an account's owner retrying while someone else
// guesses its password must not be banned for it. The name isn't logged.
func TestKratosFlows_AccountLimitLogsItsOwnMarker(t *testing.T) {
	var buf bytes.Buffer
	mw := kratosLimiter(1).KratosFlows(slog.New(slog.NewTextHandler(&buf, nil)))
	serveKratos(mw, kratosPost{kratosLogin, "10.0.7.1", jsonType, passwordLogin("alice@example.com")})
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, "10.0.7.2", jsonType, passwordLogin("alice@example.com")}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("2nd login for alice = %d, want 429", w.Code)
	}
	out := buf.String()
	if !strings.Contains(out, "marker=kratos_account_rate_limited") || !strings.Contains(out, "client_ip=10.0.7.2") {
		t.Errorf("log %q: want the account marker and the address", out)
	}
	if strings.Contains(out, "kratos_flow_rate_limited") || strings.Contains(out, "alice") {
		t.Errorf("log %q: must not carry the per-address marker or the account name", out)
	}
}
