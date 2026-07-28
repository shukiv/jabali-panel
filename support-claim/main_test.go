package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
