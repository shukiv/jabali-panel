package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os/user"
	"testing"
)

// testAuth builds an authenticator whose system lookups are faked: the user
// "acme_web" exists (uid 1000000042), is in jabali-webdav, unlocked, and its
// password is "right".
func testAuth() *authenticator {
	a := newAuthenticator()
	a.lookupUser = func(name string) (*user.User, error) {
		if name == "acme_web" {
			return &user.User{Username: "acme_web", Uid: "1000000042", Gid: "1000000042"}, nil
		}
		if name == "acme_sys" { // a would-be system-uid subaccount
			return &user.User{Username: "acme_sys", Uid: "50", Gid: "50"}, nil
		}
		if name == "root" {
			return &user.User{Username: "root", Uid: "0", Gid: "0"}, nil
		}
		return nil, user.UnknownUserError(name)
	}
	a.inGroup = func(u *user.User, g string) bool { return u.Username == "acme_web" && g == webdavAuthGroup }
	a.locked = func(name string) bool { return false }
	a.verifyPassword = func(name, pw string) bool { return name == "acme_web" && pw == "right" }
	return a
}

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func callAuth(a *authenticator, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/jabali-webdav-auth", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	return rec
}

func TestAuth_Matrix(t *testing.T) {
	cases := []struct {
		name       string
		header     string
		wantStatus int
		wantSub    string
	}{
		{"valid subaccount + correct pw", basic("acme_web", "right"), http.StatusOK, "acme_web"},
		{"correct user wrong pw", basic("acme_web", "wrong"), http.StatusUnauthorized, ""},
		{"no auth header", "", http.StatusUnauthorized, ""},
		{"empty password", basic("acme_web", ""), http.StatusUnauthorized, ""},
		{"unknown user", basic("acme_ghost", "right"), http.StatusUnauthorized, ""},
		{"system uid subaccount name", basic("acme_sys", "right"), http.StatusUnauthorized, ""},
		{"system account root", basic("root", "right"), http.StatusUnauthorized, ""},
		{"not namespaced (no underscore)", basic("acmeweb", "right"), http.StatusUnauthorized, ""},
		{"path-injection charset", basic("acme_web/../x", "right"), http.StatusUnauthorized, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := testAuth()
			rec := callAuth(a, tc.header)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("X-Webdav-Sub"); got != tc.wantSub {
				t.Fatalf("X-Webdav-Sub = %q, want %q", got, tc.wantSub)
			}
			if tc.wantStatus == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatalf("401 must carry WWW-Authenticate so clients prompt")
			}
		})
	}
}

// A locked (disabled/suspended) account must be denied even with the correct
// password, and unix_chkpwd must never run for it (JAB-256).
func TestAuth_LockedDeniedBeforeVerify(t *testing.T) {
	a := testAuth()
	a.locked = func(name string) bool { return true }
	verifyCalled := false
	a.verifyPassword = func(name, pw string) bool { verifyCalled = true; return true }
	rec := callAuth(a, basic("acme_web", "right"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("locked account: status = %d, want 401", rec.Code)
	}
	if verifyCalled {
		t.Fatal("unix_chkpwd must not run for a locked account")
	}
}

// unix_chkpwd must never be invoked for a name that fails the identity gate —
// the endpoint is not a password oracle for system accounts.
func TestAuth_NoVerifyForIneligible(t *testing.T) {
	for _, name := range []string{"root", "acme_sys", "acme_ghost", "acmeweb"} {
		a := testAuth()
		verifyCalled := false
		a.verifyPassword = func(n, pw string) bool { verifyCalled = true; return true }
		callAuth(a, basic(name, "right"))
		if verifyCalled {
			t.Fatalf("%s: unix_chkpwd must not run for an ineligible identity", name)
		}
	}
}

func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter()
	// Burst allows the first `burst` attempts, then the bucket empties.
	allowed := 0
	for i := 0; i < 20; i++ {
		if l.allow("acme_web") {
			allowed++
		}
	}
	if allowed < 1 || allowed > int(l.burst)+1 {
		t.Fatalf("allowed %d of 20 rapid attempts; want ~burst (%v)", allowed, l.burst)
	}
	// A different username has its own bucket.
	if !l.allow("acme_other") {
		t.Fatal("per-username buckets are independent; a fresh key must be allowed")
	}
}

// A legit client sending the correct password on every request (HTTP Basic
// re-sends creds; the daemon never caches) must NOT get throttled — the token
// is refunded on success. A wrong-password flood, by contrast, drains the bucket.
func TestAuth_SuccessNotThrottled_FailuresAre(t *testing.T) {
	a := testAuth()
	for i := 0; i < 50; i++ {
		if rec := callAuth(a, basic("acme_web", "right")); rec.Code != http.StatusOK {
			t.Fatalf("correct-password request %d got %d; success must not be throttled", i, rec.Code)
		}
	}
	// Now hammer with wrong passwords — the bucket drains and denials become
	// throttle-fast (still 401 either way; the point is it must not wedge).
	denied := 0
	for i := 0; i < 50; i++ {
		if rec := callAuth(a, basic("acme_web", "wrong")); rec.Code == http.StatusUnauthorized {
			denied++
		}
	}
	if denied != 50 {
		t.Fatalf("wrong-password flood: %d/50 denied, want all denied", denied)
	}
}

// shadowLocked must allow ONLY a real crypt hash and fail closed on every
// locked/disabled/unset/missing state — pinned so the deny boundary can't drift.
func TestShadowLocked(t *testing.T) {
	const shadow = "acme_web:$y$j9T$abcdef:19000:0:99999:7:::\n" +
		"acme_lck:!$y$j9T$abcdef:19000:0:99999:7:::\n" +
		"acme_bang:!:19000:0:99999:7:::\n" +
		"acme_bb:!!:19000:0:99999:7:::\n" +
		"acme_star:*:19000:0:99999:7:::\n" +
		"acme_empty::19000:0:99999:7:::\n"
	cases := []struct {
		user   string
		locked bool
	}{
		{"acme_web", false},  // real yescrypt hash → allow
		{"acme_lck", true},   // usermod -L (!hash) → deny
		{"acme_bang", true},  // bare ! → deny
		{"acme_bb", true},    // !! → deny
		{"acme_star", true},  // * (disabled) → deny
		{"acme_empty", true}, // empty → deny
		{"acme_ghost", true}, // no entry → deny (fail closed)
	}
	for _, tc := range cases {
		if got := shadowLocked(shadow, tc.user); got != tc.locked {
			t.Errorf("shadowLocked(%s) = %v, want %v", tc.user, got, tc.locked)
		}
	}
}

func TestParseBasicAuth(t *testing.T) {
	u, p, ok := parseBasicAuth(basic("acme_web", "s3cret:with:colons"))
	if !ok || u != "acme_web" || p != "s3cret:with:colons" {
		t.Fatalf("parseBasicAuth = %q %q %v", u, p, ok)
	}
	if _, _, ok := parseBasicAuth(""); ok {
		t.Fatal("empty header must not parse")
	}
	if _, _, ok := parseBasicAuth("Bearer xyz"); ok {
		t.Fatal("non-Basic scheme must not parse")
	}
}
