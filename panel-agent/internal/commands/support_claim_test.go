package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegisterSupportClaim: the agent POSTs the bundle's {url,password,meta} to
// the claim service and returns the short code (GH #357 claim-code).
func TestRegisterSupportClaim(t *testing.T) {
	var got claimIssueRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/claims" || r.Method != http.MethodPost {
			http.Error(w, "bad", http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"JAB-7QX9K2P4","expires_at":"2026-08-11T00:00:00Z"}`))
	}))
	defer ts.Close()

	code, err := registerSupportClaim(context.Background(), ts.URL, claimIssueRequest{
		URL: "https://enclosed/#pw:k", Password: "pw123", Host: "mx", NoteID: "n1", Bytes: 1000, Files: 50,
	})
	if err != nil {
		t.Fatalf("registerSupportClaim: %v", err)
	}
	if code != "JAB-7QX9K2P4" {
		t.Errorf("code = %q", code)
	}
	// The sensitive fields were forwarded so the service can hand them back on
	// authenticated redeem.
	if got.URL != "https://enclosed/#pw:k" || got.Password != "pw123" || got.Files != 50 {
		t.Errorf("forwarded payload mismatch: %+v", got)
	}
}

// A non-200 from the claim service is an error (caller falls back to
// link+password, leaving ClaimCode empty).
func TestRegisterSupportClaim_ErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer ts.Close()
	if _, err := registerSupportClaim(context.Background(), ts.URL, claimIssueRequest{URL: "u", Password: "p"}); err == nil {
		t.Error("expected an error on non-200 claim status")
	}
}
