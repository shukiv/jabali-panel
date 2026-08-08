package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testServer(now time.Time) (*server, *httptest.Server) {
	clock := now
	srv := &server{
		store: newStore(24 * time.Hour),
		token: "s3cret-team-token",
		limit: newRateLimiter(1000),
		now:   func() time.Time { return clock },
	}
	return srv, httptest.NewServer(srv.routes())
}

func issue(t *testing.T, ts *httptest.Server, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(ts.URL+"/claims", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func redeem(t *testing.T, ts *httptest.Server, code, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/claims/"+code, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestIssueAndRedeem(t *testing.T) {
	_, ts := testServer(time.Unix(1_700_000_000, 0))
	defer ts.Close()

	resp := issue(t, ts, `{"url":"https://enclosed/#pw:k","password":"pw123","host":"mx","note_id":"n1","byte_count":468480,"file_count":50}`)
	if resp.StatusCode != 200 {
		t.Fatalf("issue status = %d", resp.StatusCode)
	}
	var ir struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&ir)
	if !strings.HasPrefix(ir.Code, "JAB-") || len(ir.Code) != 4+codeLen {
		t.Fatalf("bad code %q", ir.Code)
	}

	// Redeem with the team token → the full payload.
	rr := redeem(t, ts, ir.Code, "s3cret-team-token")
	if rr.StatusCode != 200 {
		t.Fatalf("redeem status = %d", rr.StatusCode)
	}
	var p redeemPayload
	_ = json.NewDecoder(rr.Body).Decode(&p)
	if p.URL != "https://enclosed/#pw:k" || p.Password != "pw123" || p.Files != 50 {
		t.Fatalf("payload mismatch: %+v", p)
	}
}

func TestRedeemRequiresToken(t *testing.T) {
	_, ts := testServer(time.Unix(1_700_000_000, 0))
	defer ts.Close()
	var ir struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(issue(t, ts, `{"url":"u","password":"p"}`).Body).Decode(&ir)

	// No token → 401.
	if r := redeem(t, ts, ir.Code, ""); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-token redeem = %d, want 401", r.StatusCode)
	}
	// Wrong token → 401.
	if r := redeem(t, ts, ir.Code, "wrong"); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-token redeem = %d, want 401", r.StatusCode)
	}
}

func TestRedeemUnknownAndExpired(t *testing.T) {
	srv, ts := testServer(time.Unix(1_700_000_000, 0))
	defer ts.Close()
	// Unknown code → 404 (with valid auth).
	if r := redeem(t, ts, "JAB-ZZZZZZZZ", "s3cret-team-token"); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown redeem = %d, want 404", r.StatusCode)
	}
	// Issue, then advance the clock past TTL → 404.
	var ir struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(issue(t, ts, `{"url":"u","password":"p"}`).Body).Decode(&ir)
	srv.now = func() time.Time { return time.Unix(1_700_000_000, 0).Add(48 * time.Hour) }
	if r := redeem(t, ts, ir.Code, "s3cret-team-token"); r.StatusCode != http.StatusNotFound {
		t.Errorf("expired redeem = %d, want 404", r.StatusCode)
	}
}

func TestIssueValidatesRequired(t *testing.T) {
	_, ts := testServer(time.Unix(1_700_000_000, 0))
	defer ts.Close()
	if r := issue(t, ts, `{"url":"","password":"p"}`); r.StatusCode != http.StatusBadRequest {
		t.Errorf("missing url = %d, want 400", r.StatusCode)
	}
	if r := issue(t, ts, `{"url":"u","password":""}`); r.StatusCode != http.StatusBadRequest {
		t.Errorf("missing password = %d, want 400", r.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	srv := &server{
		store: newStore(time.Hour),
		token: "t",
		limit: newRateLimiter(2), // 2/min
		now:   time.Now,
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	codes := 0
	for i := 0; i < 5; i++ {
		if issue(t, ts, `{"url":"u","password":"p"}`).StatusCode == 200 {
			codes++
		}
	}
	// The burst allows ~perMin issues, then 429s. Must not allow all 5.
	if codes >= 5 {
		t.Errorf("rate limiter let all %d through", codes)
	}
	if codes == 0 {
		t.Errorf("rate limiter blocked everything")
	}
}

// clientIP must NOT trust X-Forwarded-For from a non-loopback peer. Trusting
// it from anyone made the per-IP rate limit meaningless (rotate the header to
// bypass the cap on the open POST /claims) and, because each distinct value
// allocated a permanent bucket entry, turned the same trick into unbounded
// heap growth. :8088 is directly reachable, and nothing guarantees the service
// is proxy-fronted.
func TestClientIPIgnoresXFFFromUntrustedPeer(t *testing.T) {
	r := httptest.NewRequest("POST", "/claims", nil)
	r.RemoteAddr = "203.0.113.9:51000"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the real peer 203.0.113.9 — a spoofed "+
			"X-Forwarded-For must not become the rate-limit key", got)
	}
}

// A loopback peer IS the local reverse proxy, so its XFF is authoritative.
func TestClientIPTrustsXFFFromLoopback(t *testing.T) {
	r := httptest.NewRequest("POST", "/claims", nil)
	r.RemoteAddr = "127.0.0.1:51000"
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	if got := clientIP(r); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want 198.51.100.7 from the trusted proxy", got)
	}
}

// Rotating the header must not let one source outrun the cap.
func TestRateLimitNotBypassableByRotatingXFF(t *testing.T) {
	rl := newRateLimiter(3)
	allowed := 0
	for i := 0; i < 20; i++ {
		r := httptest.NewRequest("POST", "/claims", nil)
		r.RemoteAddr = "203.0.113.9:51000"
		r.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i))
		if rl.allow(clientIP(r)) {
			allowed++
		}
	}
	if allowed > 3 {
		t.Fatalf("allowed %d requests from one peer rotating XFF, cap is 3", allowed)
	}
}

// The bucket map must not keep an entry per key forever.
func TestRateLimiterEvictsIdleBuckets(t *testing.T) {
	rl := newRateLimiter(30)
	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	rl.now = func() time.Time { return base }
	for i := 0; i < 500; i++ {
		rl.allow("198.51.100." + strconv.Itoa(i))
	}
	if len(rl.bucket) < 100 {
		t.Fatalf("expected many buckets while active, got %d", len(rl.bucket))
	}
	// Long after everything went idle, one more call should sweep the rest.
	rl.now = func() time.Time { return base.Add(time.Hour) }
	rl.allow("203.0.113.1")
	if len(rl.bucket) > 2 {
		t.Fatalf("idle buckets not evicted: %d remain — the map grows without "+
			"bound from client-controlled keys", len(rl.bucket))
	}
}

// The claim store must refuse new entries at capacity rather than growing
// until the process is OOM-killed.
func TestStorePutRefusesWhenFull(t *testing.T) {
	s := newStore(14 * 24 * time.Hour)
	now := time.Now()
	for i := 0; i < maxClaims; i++ {
		if _, _, err := s.put(redeemPayload{Host: "h"}, now); err != nil {
			t.Fatalf("unexpected error filling store at %d: %v", i, err)
		}
	}
	_, _, err := s.put(redeemPayload{Host: "h"}, now)
	if !errors.Is(err, errStoreFull) {
		t.Fatalf("put past capacity returned %v, want errStoreFull", err)
	}
}
