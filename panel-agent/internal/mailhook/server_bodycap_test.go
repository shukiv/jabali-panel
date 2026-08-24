package mailhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// nilSource never returns a disclaimer — the request never reaches the parser
// path, which is fine: this test is about the body cap, not the rewrite.
type nilSource struct{}

func (nilSource) ForDomain(context.Context, string) (Disclaimer, bool, error) {
	return Disclaimer{}, false, nil
}

func newTestHandler() *Handler {
	return &Handler{Source: nilSource{}, Token: "", Log: slog.New(slog.DiscardHandler)}
}

// JAB-384: a request body larger than MaxRequestBytes must not be read
// unbounded — MaxBytesReader trips, the JSON decode fails, and the handler
// fails open (action:accept, deliver unchanged) rather than OOM'ing.
func TestServeHTTP_OversizedBody_FailsOpen(t *testing.T) {
	orig := MaxRequestBytes
	MaxRequestBytes = 256 // shrink so the test stays cheap
	defer func() { MaxRequestBytes = orig }()

	body := `{"envelope":{"from":{"address":"a@b.com"}},"message":{"contents":"` +
		strings.Repeat("A", 1024) + `"}}` // > 256 bytes

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	newTestHandler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("oversized body should still respond 200 (fail open), got %d", w.Code)
	}
	var resp hookResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (%s)", err, w.Body.String())
	}
	if resp.Action != "accept" {
		t.Errorf("oversized body must deliver unchanged (accept), got %q", resp.Action)
	}
}

// A normal-sized body under the cap decodes and is processed (here it accepts
// because nilSource has no disclaimer) — proving the cap doesn't break the
// happy path.
func TestServeHTTP_NormalBody_UnderCap_OK(t *testing.T) {
	body := `{"envelope":{"from":{"address":"a@b.com"}},"message":{"contents":"hello"}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	newTestHandler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	out, _ := io.ReadAll(w.Result().Body)
	var resp hookResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Action != "accept" {
		t.Errorf("action = %q, want accept", resp.Action)
	}
}
