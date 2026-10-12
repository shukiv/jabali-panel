package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// A migration's SSH connect failure is the admin's to fix (host, credentials,
// host key), so its detail must reach the browser. It answers 422, not 502:
// Cloudflare replaces an origin's 502 with its own error page, which hid the
// detail on a panel behind it (GH #2077).
func TestRespondMigrateConnectErr_KeepsTheDetailPastAProxy(t *testing.T) {
	c, w := newGateCtx()

	respondMigrateConnectErr(c, errors.New("ssh: handshake failed: ssh: unable to authenticate"))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["error"] != "connect_failed" || out["detail"] != "ssh: handshake failed: ssh: unable to authenticate" {
		t.Fatalf("body %s, want connect_failed with the SSH error", w.Body.String())
	}
}

// A migration plugin handshake that fails says why (the source site's error),
// as 422 so Cloudflare doesn't replace it (GH #2077).
func TestRespondMigrateHandshakeErr_KeepsTheDetailPastAProxy(t *testing.T) {
	c, w := newGateCtx()

	respondMigrateHandshakeErr(c, errors.New("plugin ping: 401 Unauthorized"))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["error"] != "handshake_failed" || out["detail"] != "cannot reach the plugin — check the site URL and token: plugin ping: 401 Unauthorized" {
		t.Fatalf("body %s, want handshake_failed with the plugin's error", w.Body.String())
	}
}
