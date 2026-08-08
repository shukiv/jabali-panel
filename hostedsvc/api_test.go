package hostedsvc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// fakeDNS records every write; the maps are keyed by label so tests can
// assert exactly which label a call touched.
type fakeDNS struct {
	mu         sync.Mutex
	a          map[string]string
	wildcard   map[string]string
	challenges map[string]string
}

func newFakeDNS() *fakeDNS {
	return &fakeDNS{a: map[string]string{}, wildcard: map[string]string{}, challenges: map[string]string{}}
}
func (f *fakeDNS) EnsureA(_ context.Context, label, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.a[label] = ip
	return nil
}
func (f *fakeDNS) EnsureWildcardA(_ context.Context, label, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wildcard[label] = ip
	return nil
}
func (f *fakeDNS) SetChallenge(_ context.Context, label, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.challenges[label] = v
	return nil
}
func (f *fakeDNS) ClearChallenge(_ context.Context, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.challenges, label)
	return nil
}
func (f *fakeDNS) RemoveLabel(_ context.Context, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.a, label)
	delete(f.wildcard, label)
	delete(f.challenges, label)
	return nil
}

type fakeMailer struct {
	mu    sync.Mutex
	codes map[string]string // email -> last code
	fail  bool
}

func (m *fakeMailer) SendCode(email, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return context.DeadlineExceeded
	}
	if m.codes == nil {
		m.codes = map[string]string{}
	}
	m.codes[email] = code
	return nil
}

func newTestAPI(t *testing.T) (*API, *fakeDNS, *fakeMailer) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1) // in-memory sqlite: one conn = one database
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	dns := newFakeDNS()
	mailer := &fakeMailer{}
	return &API{Store: store, DNS: dns, Mailer: mailer, Log: slog.New(slog.DiscardHandler)}, dns, mailer
}

// call fakes an nginx-proxied request: peer is loopback, X-Real-IP carries
// the "observed" client address.
func call(t *testing.T, h http.Handler, path, sourceIP string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.RemoteAddr = "127.0.0.1:33000"
	r.Header.Set("X-Real-IP", sourceIP)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func registerAndClaim(t *testing.T, a *API, mailer *fakeMailer, email, ip string) (label, token string) {
	t.Helper()
	h := a.Routes()
	if w, _ := call(t, h, "/v1/register", ip, RegisterRequest{Email: email}); w.Code != 200 {
		t.Fatalf("register: HTTP %d %s", w.Code, w.Body.String())
	}
	w, out := call(t, h, "/v1/claim", ip, ClaimRequest{Email: email, Code: mailer.codes[email]})
	if w.Code != 200 {
		t.Fatalf("claim: HTTP %d %s", w.Code, w.Body.String())
	}
	return out["label"].(string), out["token"].(string)
}

func TestClaimFlow(t *testing.T) {
	a, dns, mailer := newTestAPI(t)
	label, token := registerAndClaim(t, a, mailer, "op@example.com", "45.79.1.9")

	if label != "45-79-1-9" {
		t.Errorf("label = %q", label)
	}
	if dns.a[label] != "45.79.1.9" {
		t.Errorf("A record = %q", dns.a[label])
	}
	if dns.wildcard[label] != "45.79.1.9" {
		t.Errorf("wildcard A missing: %q (mail.<label>/previews need it)", dns.wildcard[label])
	}
	if len(token) != 64 {
		t.Errorf("token length = %d", len(token))
	}

	// Code is single-use.
	w, _ := call(t, a.Routes(), "/v1/claim", "45.79.1.9", ClaimRequest{Email: "op@example.com", Code: mailer.codes["op@example.com"]})
	if w.Code != http.StatusForbidden {
		t.Errorf("code reuse: HTTP %d", w.Code)
	}
}

func TestClaimCollisionSuffix(t *testing.T) {
	a, dns, mailer := newTestAPI(t)
	l1, _ := registerAndClaim(t, a, mailer, "one@example.com", "45.79.1.9")
	l2, _ := registerAndClaim(t, a, mailer, "two@example.com", "45.79.1.9")
	if l1 != "45-79-1-9" || l2 != "45-79-1-9-b" {
		t.Errorf("labels = %q, %q", l1, l2)
	}
	if dns.a[l2] != "45.79.1.9" {
		t.Errorf("second A record missing")
	}
}

func TestClaimRefusesPrivateSource(t *testing.T) {
	a, _, mailer := newTestAPI(t)
	h := a.Routes()
	call(t, h, "/v1/register", "192.168.1.50", RegisterRequest{Email: "nat@example.com"})
	w, _ := call(t, h, "/v1/claim", "192.168.1.50", ClaimRequest{Email: "nat@example.com", Code: mailer.codes["nat@example.com"]})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("private source: HTTP %d %s", w.Code, w.Body.String())
	}
}

// The ACME invariant: a token can only ever write the challenge under ITS
// OWN label — there is no label parameter to abuse, and hostile TXT content
// stays inert.
func TestAcmeChallengeScopedToOwnLabel(t *testing.T) {
	a, dns, mailer := newTestAPI(t)
	labelA, tokenA := registerAndClaim(t, a, mailer, "a@example.com", "45.79.1.9")
	labelB, _ := registerAndClaim(t, a, mailer, "b@example.com", "51.15.2.7")

	h := a.Routes()
	hostile := `x" 0 IN TXT "pwn._acme-challenge.` + labelB
	w, _ := call(t, h, "/v1/acme/present", "45.79.1.9", AcmePresentRequest{Token: tokenA, TXT: hostile})
	if w.Code != 200 {
		t.Fatalf("present: HTTP %d %s", w.Code, w.Body.String())
	}
	if _, ok := dns.challenges[labelB]; ok {
		t.Fatal("token A wrote a challenge under label B")
	}
	if dns.challenges[labelA] != hostile {
		t.Fatal("own-label challenge missing (value must be stored verbatim, not parsed)")
	}

	// Cleanup also only touches the caller's label.
	call(t, h, "/v1/acme/cleanup", "45.79.1.9", TokenRequest{Token: tokenA})
	if _, ok := dns.challenges[labelA]; ok {
		t.Fatal("cleanup left own challenge")
	}
}

func TestReleaseRevokesAndBlocksToken(t *testing.T) {
	a, dns, mailer := newTestAPI(t)
	label, token := registerAndClaim(t, a, mailer, "rel@example.com", "45.79.1.9")

	h := a.Routes()
	if w, _ := call(t, h, "/v1/release", "45.79.1.9", TokenRequest{Token: token}); w.Code != 200 {
		t.Fatalf("release failed")
	}
	if _, ok := dns.a[label]; ok {
		t.Error("A record survived release")
	}
	if w, _ := call(t, h, "/v1/heartbeat", "45.79.1.9", TokenRequest{Token: token}); w.Code != http.StatusForbidden {
		t.Errorf("revoked token still works: HTTP %d", w.Code)
	}
	// The label name is burned — a new claim from the same IP gets a suffix.
	l2, _ := registerAndClaim(t, a, mailer, "rel2@example.com", "45.79.1.9")
	if l2 != "45-79-1-9-b" {
		t.Errorf("burned label reissued: %q", l2)
	}
}

func TestHeartbeatDetectsIPMove(t *testing.T) {
	a, _, mailer := newTestAPI(t)
	_, token := registerAndClaim(t, a, mailer, "mv@example.com", "45.79.1.9")

	h := a.Routes()
	_, out := call(t, h, "/v1/heartbeat", "45.79.1.9", TokenRequest{Token: token})
	if out["ip_moved"] == true {
		t.Error("same IP flagged as moved")
	}
	_, out = call(t, h, "/v1/heartbeat", "51.15.2.7", TokenRequest{Token: token})
	if out["ip_moved"] != true {
		t.Error("moved IP not flagged")
	}
}

func TestCodeBruteForceCap(t *testing.T) {
	a, _, mailer := newTestAPI(t)
	h := a.Routes()
	call(t, h, "/v1/register", "45.79.1.9", RegisterRequest{Email: "bf@example.com"})

	var last int
	for i := 0; i < codeMaxAttempts+2; i++ {
		w, _ := call(t, h, "/v1/claim", "45.79.1.9", ClaimRequest{Email: "bf@example.com", Code: "000000"})
		last = w.Code
	}
	if last != http.StatusForbidden && last != http.StatusTooManyRequests {
		t.Fatalf("cap not enforced, last = %d", last)
	}
	// Even the REAL code is dead after the attempt cap.
	w, _ := call(t, h, "/v1/claim", "45.79.1.9", ClaimRequest{Email: "bf@example.com", Code: mailer.codes["bf@example.com"]})
	if w.Code == 200 {
		t.Fatal("real code survived brute-force lockout")
	}
}

func TestRegisterResendThrottle(t *testing.T) {
	a, _, _ := newTestAPI(t)
	h := a.Routes()
	if w, _ := call(t, h, "/v1/register", "45.79.1.9", RegisterRequest{Email: "th@example.com"}); w.Code != 200 {
		t.Fatal("first register failed")
	}
	if w, _ := call(t, h, "/v1/register", "45.79.1.9", RegisterRequest{Email: "th@example.com"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("resend not throttled: HTTP %d", w.Code)
	}
}

func TestEmailLabelCap(t *testing.T) {
	a, _, _ := newTestAPI(t)
	h := a.Routes()
	// Seed codes straight into the store (the register resend-throttle is
	// per-minute and tested separately); claim from a distinct IP each time.
	seed := func() {
		t.Helper()
		if err := a.Store.PutCode("cap@example.com", "111111"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Store.db.Exec(`UPDATE email_codes SET sent_at = 0 WHERE email = 'cap@example.com'`); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < emailLabelCap; i++ {
		seed()
		ip := fmt.Sprintf("45.79.1.%d", i+1)
		w, _ := call(t, h, "/v1/claim", ip, ClaimRequest{Email: "cap@example.com", Code: "111111"})
		if w.Code != 200 {
			t.Fatalf("claim %d: HTTP %d %s", i, w.Code, w.Body.String())
		}
	}
	seed()
	w, _ := call(t, h, "/v1/claim", "51.15.2.99", ClaimRequest{Email: "cap@example.com", Code: "111111"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("cap not enforced: HTTP %d", w.Code)
	}
}
