package middleware_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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
// they name, twice an address's, so guessing one account's password from
// many addresses is held to what two addresses may try.

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

// kratosLimiter allows burst POSTs per address and twice that per account,
// no refill.
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

// addr is the n-th test address of a /24.
func addr(prefix string, n int) string { return fmt.Sprintf("%s.%d", prefix, n) }

func TestKratosFlows_AccountBudgetHoldsAcrossAddresses(t *testing.T) {
	mw := kratosLimiter(3).KratosFlows(nil)
	for n := 1; n <= 6; n++ {
		if w, _ := serveKratos(mw, kratosPost{kratosLogin, addr("10.0.1", n), jsonType, passwordLogin("alice")}); w.Code != http.StatusOK {
			t.Fatalf("login %d for alice = %d, want 200 within the account's budget of 6", n, w.Code)
		}
	}
	w, _ := serveKratos(mw, kratosPost{kratosLogin, addr("10.0.1", 7), jsonType, passwordLogin("alice")})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("7th login for alice, from a 7th address = %d, want 429", w.Code)
	}
	// The same answer as the per-address limit: nothing tells a known
	// account from an unknown name.
	if w.Header().Get("Retry-After") != "60" || strings.TrimSpace(w.Body.String()) != `{"error":"rate_limited"}` {
		t.Errorf("429 = Retry-After %q body %q, want 60 and rate_limited", w.Header().Get("Retry-After"), w.Body.String())
	}
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, addr("10.0.1", 7), jsonType, passwordLogin("bob")}); w.Code != http.StatusOK {
		t.Errorf("login for bob = %d, want 200: each account has its own budget", w.Code)
	}
}

// One address at its own limit can't spend all of an account's budget: the
// owner, from another address, still gets in.
func TestKratosFlows_OneAddressCannotLockAnAccount(t *testing.T) {
	mw := kratosLimiter(3).KratosFlows(nil)
	for i := 0; i < 8; i++ {
		serveKratos(mw, kratosPost{kratosLogin, "10.0.8.1", jsonType, passwordLogin("alice")})
	}
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, "10.0.8.2", jsonType, passwordLogin("alice")}); w.Code != http.StatusOK {
		t.Errorf("alice's login from her own address, after another address hit its limit = %d, want 200", w.Code)
	}
}

func TestKratosFlows_AccountNameIsNormalised(t *testing.T) {
	mw := kratosLimiter(2).KratosFlows(nil)
	for n, name := range []string{"Alice@Example.com", " alice@example.com", "ALICE@EXAMPLE.COM\t", "alice@example.com "} {
		if w, _ := serveKratos(mw, kratosPost{kratosLogin, addr("10.0.2", n+1), jsonType, passwordLogin(name)}); w.Code != http.StatusOK {
			t.Fatalf("login %q = %d, want 200", name, w.Code)
		}
	}
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, addr("10.0.2", 9), jsonType, passwordLogin("alice@example.com")}); w.Code != http.StatusTooManyRequests {
		t.Errorf("5th login for the same account, spelled another way = %d, want 429", w.Code)
	}
}

// Kratos's other fields that name the account share its budget, and must
// agree with the usual one.
func TestKratosFlows_EveryFieldThatNamesTheAccountCounts(t *testing.T) {
	mw := kratosLimiter(1).KratosFlows(nil)
	legacy := kratosBody(map[string]string{"method": "password", "password_identifier": "alice", "password": "x"})
	for n := 1; n <= 2; n++ {
		if w, _ := serveKratos(mw, kratosPost{kratosLogin, addr("10.0.9", n), jsonType, legacy}); w.Code != http.StatusOK {
			t.Fatalf("password_identifier login %d = %d, want 200", n, w.Code)
		}
	}
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, addr("10.0.9", 3), jsonType, passwordLogin("Alice")}); w.Code != http.StatusTooManyRequests {
		t.Errorf("identifier login after two password_identifier ones = %d, want 429 (one account)", w.Code)
	}

	v2 := func(field string) string {
		return kratosBody(map[string]string{"method": "code", field: "bob@example.com"})
	}
	for n, f := range []string{"recovery_address", "recovery_confirm_address"} {
		if w, _ := serveKratos(mw, kratosPost{kratosRecovery, addr("10.0.10", n+1), jsonType, v2(f)}); w.Code != http.StatusOK {
			t.Fatalf("recovery with %s = %d, want 200", f, w.Code)
		}
	}
	if w, _ := serveKratos(mw, kratosPost{kratosRecovery, addr("10.0.10", 3), jsonType, v2("email")}); w.Code != http.StatusTooManyRequests {
		t.Errorf("recovery by email after two by the v2 fields = %d, want 429 (one account)", w.Code)
	}

	for name, p := range map[string]kratosPost{
		"login":    {kratosLogin, "10.0.11.1", jsonType, `{"identifier":"decoy","password_identifier":"carol","password":"x"}`},
		"recovery": {kratosRecovery, "10.0.11.2", jsonType, `{"email":"decoy","recovery_confirm_address":"carol"}`},
	} {
		if w, _ := serveKratos(mw, p); w.Code != http.StatusBadRequest {
			t.Errorf("%s naming two accounts = %d, want 400", name, w.Code)
		}
	}
}

// The second-factor steps and the recovery code step name no account: only
// the per-address limit applies to them.
func TestKratosFlows_StepsThatNameNoAccountAreNotAccountLimited(t *testing.T) {
	mw := kratosLimiter(1).KratosFlows(nil)
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
	mw := kratosLimiter(1).KratosFlows(nil)
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
	serveKratos(mw, kratosPost{kratosLogin, "10.0.7.2", jsonType, passwordLogin("alice@example.com")})
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, "10.0.7.3", jsonType, passwordLogin("alice@example.com")}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd login for alice = %d, want 429", w.Code)
	}
	out := buf.String()
	if !strings.Contains(out, "marker=kratos_account_rate_limited") || !strings.Contains(out, "client_ip=10.0.7.3") {
		t.Errorf("log %q: want the account marker and the address", out)
	}
	if strings.Contains(out, "kratos_flow_rate_limited") || strings.Contains(out, "alice") {
		t.Errorf("log %q: must not carry the per-address marker or the account name", out)
	}
}

// kratosLoginStandIn answers a login with the password "right" as Kratos does: 200
// and a session cookie. Anything else is 400 without one.
func kratosLoginStandIn(c *gin.Context) {
	var b struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(c.Request.Body).Decode(&b)
	if b.Password == "right" {
		http.SetCookie(c.Writer, &http.Cookie{Name: "ory_kratos_session", Value: "s", Path: "/"})
		c.Status(http.StatusOK)
		return
	}
	c.Status(http.StatusBadRequest)
}

func serveKratosTo(mw, next gin.HandlerFunc, p kratosPost) int {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Any("/*any", mw, next)
	req := httptest.NewRequest(http.MethodPost, p.path, strings.NewReader(p.body))
	req.Header.Set("Content-Type", p.contentType)
	req.RemoteAddr = p.ip + ":54321"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// An address whose login to the account set a session keeps its way in while
// other addresses use up the account's budget; a new address doesn't, and a
// failed login doesn't count as signing in.
func TestKratosFlows_AnAddressThatSignedInKeepsItsWay(t *testing.T) {
	mw := kratosLimiter(2).KratosFlows(nil)
	login := func(n int, password string) int {
		body := kratosBody(map[string]string{"method": "password", "identifier": "alice", "password": password})
		return serveKratosTo(mw, kratosLoginStandIn, kratosPost{kratosLogin, addr("10.0.12", n), jsonType, body})
	}
	if code := login(1, "right"); code != http.StatusOK {
		t.Fatalf("alice's login = %d, want 200", code)
	}
	login(2, "wrong")
	login(2, "wrong")
	login(3, "wrong")
	if code := login(4, "wrong"); code != http.StatusTooManyRequests {
		t.Fatalf("login with the account's budget used up = %d, want 429", code)
	}
	if code := login(1, "right"); code != http.StatusOK {
		t.Errorf("alice's login from the address she signed in from = %d, want 200", code)
	}
	if code := login(5, "right"); code != http.StatusTooManyRequests {
		t.Errorf("a login from a new address = %d, want 429", code)
	}
	if code := login(3, "wrong"); code != http.StatusTooManyRequests {
		t.Errorf("a login from an address whose login failed = %d, want 429", code)
	}
}

// Kratos takes the form from the body; a name in the query string is refused
// rather than left out of the account budget.
func TestKratosFlows_RefusesAccountNamesInTheQueryString(t *testing.T) {
	mw := kratosLimiter(100).KratosFlows(nil)
	for name, c := range map[string]struct {
		p    kratosPost
		want int
	}{
		"identifier":          {kratosPost{kratosLogin + "&identifier=alice", "10.0.13.1", jsonType, passwordLogin("bob")}, http.StatusBadRequest},
		"password_identifier": {kratosPost{kratosLogin + "&Password_Identifier=alice", "10.0.13.2", "", ""}, http.StatusBadRequest},
		"email":               {kratosPost{kratosRecovery + "&email=alice", "10.0.13.3", jsonType, `{"method":"code"}`}, http.StatusBadRequest},
		"other parameters":    {kratosPost{kratosLogin + "&return_to=%2Fdashboard", "10.0.13.4", jsonType, passwordLogin("bob")}, http.StatusOK},
	} {
		if w, _ := serveKratos(mw, c.p); w.Code != c.want {
			t.Errorf("%s: %d, want %d", name, w.Code, c.want)
		}
	}
}

func TestKratosFlows_RefusesANameThatIsNotAString(t *testing.T) {
	mw := kratosLimiter(100).KratosFlows(nil)
	for i, body := range []string{`{"identifier":123}`, `{"identifier":{"a":"alice"}}`, `{"identifier":["alice"]}`, `{"identifier":true}`} {
		if w, _ := serveKratos(mw, kratosPost{kratosLogin, addr("10.0.14", i+1), jsonType, body}); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, w.Code)
		}
	}
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, "10.0.14.9", jsonType, `{"identifier":null,"method":"totp"}`}); w.Code != http.StatusOK {
		t.Errorf("a null name = %d, want 200 (names no account)", w.Code)
	}
}

// Recovery addresses compare as utf8mb4_unicode_ci: case, accents and width
// don't count, and ß is ss. Spellings that are one address to Kratos share
// one budget.
func TestKratosFlows_NamesFoldAsTheirColumnCompares(t *testing.T) {
	mw := kratosLimiter(1).KratosFlows(nil)
	recoverAs := func(n int, email string) int {
		w, _ := serveKratos(mw, kratosPost{kratosRecovery, addr("10.0.15", n), jsonType, kratosBody(map[string]string{"method": "code", "email": email})})
		return w.Code
	}
	if recoverAs(1, "Alícé@Example.com") != http.StatusOK || recoverAs(2, "ａlice@example.com") != http.StatusOK {
		t.Fatal("the first two recoveries should pass")
	}
	if code := recoverAs(3, "alice@example.com"); code != http.StatusTooManyRequests {
		t.Errorf("3rd recovery for one address, spelled three ways = %d, want 429", code)
	}
	if recoverAs(4, "straße@example.com") != http.StatusOK || recoverAs(5, "STRASSE@example.com") != http.StatusOK {
		t.Fatal("the first two recoveries for straße should pass")
	}
	if code := recoverAs(6, "strasse@example.com"); code != http.StatusTooManyRequests {
		t.Errorf("3rd recovery for straße/strasse = %d, want 429", code)
	}
}

func TestKratosFlows_TheAPILoginPathIsAccountLimitedToo(t *testing.T) {
	mw := kratosLimiter(1).KratosFlows(nil)
	const api = "/.ory/self-service/login/api?flow=x"
	serveKratos(mw, kratosPost{api, "10.0.16.1", jsonType, passwordLogin("alice")})
	serveKratos(mw, kratosPost{api, "10.0.16.2", jsonType, passwordLogin("alice")})
	if w, _ := serveKratos(mw, kratosPost{api, "10.0.16.3", jsonType, passwordLogin("alice")}); w.Code != http.StatusTooManyRequests {
		t.Errorf("3rd API login for alice = %d, want 429", w.Code)
	}
	if w, _ := serveKratos(mw, kratosPost{kratosLogin, "10.0.16.4", "application/json;x", passwordLogin("bob")}); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a malformed JSON content type = %d, want 415", w.Code)
	}
}

// A chunked body (no Content-Length) is read for the account and passed on
// whole.
func TestKratosFlows_PassesAChunkedBodyOn(t *testing.T) {
	mw := kratosLimiter(5).KratosFlows(nil)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var got string
	r.Any("/*any", mw, func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		got = string(b)
		c.Status(http.StatusOK)
	})
	body := passwordLogin("alice")
	req := httptest.NewRequest(http.MethodPost, kratosLogin, strings.NewReader(body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", jsonType)
	req.RemoteAddr = "10.0.17.1:54321"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || got != body {
		t.Errorf("code %d, next handler read %q, want 200 and %q", w.Code, got, body)
	}
}
