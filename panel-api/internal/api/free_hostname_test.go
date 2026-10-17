package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func withFakeService(t *testing.T, handler http.HandlerFunc) func() {
	t.Helper()
	svc := httptest.NewServer(handler)
	oldAPI, oldHTTP := freeHostnameAPI, freeHostnameHTTP
	freeHostnameAPI, freeHostnameHTTP = svc.URL, svc.Client()
	return func() {
		freeHostnameAPI, freeHostnameHTTP = oldAPI, oldHTTP
		svc.Close()
	}
}

func postFH(t *testing.T, r http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestFreeHostnameRegister_ProxiesToService(t *testing.T) {
	var gotPath, gotBody string
	defer withFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(`{"ok":true}`))
	})()

	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1}}
	router := settingsRouter(true, repo, agent.NewMockClient())
	w := postFH(t, router, "/api/v1/admin/settings/free-hostname/register", `{"email":"op@example.com"}`)
	if w.Code != 200 {
		t.Fatalf("register HTTP %d: %s", w.Code, w.Body.String())
	}
	if gotPath != "/v1/register" || !strings.Contains(gotBody, "op@example.com") {
		t.Errorf("proxied path=%q body=%q", gotPath, gotBody)
	}
}

func TestFreeHostnameRegister_RejectsBadEmail(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1}}
	router := settingsRouter(true, repo, agent.NewMockClient())
	w := postFH(t, router, "/api/v1/admin/settings/free-hostname/register", `{"email":"nope"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad email HTTP %d, want 422", w.Code)
	}
}

func TestFreeHostnameClaim_PersistsAndSwitchesHostname(t *testing.T) {
	defer withFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"label":"45-79-1-9","fqdn":"45-79-1-9.jabalihosted.com","token":"tok123"}`))
	})()

	mockAgent := agent.NewMockClient().
		On("hostname.free.apply", map[string]any{"ok": true}).
		On("system.set_hostname", map[string]any{"ok": true})
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, Hostname: "old.local"}}
	router := settingsRouter(true, repo, mockAgent)

	w := postFH(t, router, "/api/v1/admin/settings/free-hostname/claim", `{"email":"op@example.com","code":"123456"}`)
	if w.Code != 200 {
		t.Fatalf("claim HTTP %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["fqdn"] != "45-79-1-9.jabalihosted.com" {
		t.Errorf("fqdn = %v", out["fqdn"])
	}

	var appliedToken string
	for _, call := range mockAgent.Calls() {
		if call.Command == "hostname.free.apply" {
			var m map[string]any
			_ = json.Unmarshal(call.Params, &m)
			appliedToken, _ = m["token"].(string)
		}
	}
	if appliedToken != "tok123" {
		t.Errorf("apply did not carry the token, got %q", appliedToken)
	}
	if repo.getResult.Hostname != "45-79-1-9.jabalihosted.com" {
		t.Errorf("hostname not switched: %q", repo.getResult.Hostname)
	}
}

func TestFreeHostnameClaim_BadCodeSurfacesReasonNoAgentCall(t *testing.T) {
	defer withFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"code_invalid","message":"code wrong or expired"}`))
	})()

	mockAgent := agent.NewMockClient()
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1}}
	router := settingsRouter(true, repo, mockAgent)
	w := postFH(t, router, "/api/v1/admin/settings/free-hostname/claim", `{"email":"op@example.com","code":"000000"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad code HTTP %d, want 422", w.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["error"] != "code_invalid" {
		t.Errorf("error = %v, want code_invalid", out["error"])
	}
	if len(mockAgent.Calls()) != 0 {
		t.Error("no agent call should happen on a failed claim")
	}
}

// Claiming a free hostname is a rename like any other: the landing vhost is
// re-pointed and set_hostname gets the public IP (the same plan as the PATCH).
func TestFreeHostnameClaim_AppliesTheHostnamePlan(t *testing.T) {
	defer withFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"label":"45-79-1-9","fqdn":"45-79-1-9.jabalihosted.com","token":"tok123"}`))
	})()
	mockAgent := agent.NewMockClient().
		On("hostname.free.apply", map[string]any{"ok": true}).
		On("system.set_hostname", map[string]any{"ok": true}).
		On("nginx.panel_landing_rehost", map[string]any{"ok": true})
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, Hostname: "old.local", PublicIPv4: "45.79.1.9"}}
	router := settingsRouter(true, repo, mockAgent)

	if w := postFH(t, router, "/api/v1/admin/settings/free-hostname/claim", `{"email":"op@example.com","code":"123456"}`); w.Code != 200 {
		t.Fatalf("claim HTTP %d: %s", w.Code, w.Body.String())
	}
	got := map[string]map[string]any{}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(got) < 2 {
		for _, call := range mockAgent.Calls() {
			if call.Command == "system.set_hostname" || call.Command == "nginx.panel_landing_rehost" {
				var m map[string]any
				_ = json.Unmarshal(call.Params, &m)
				got[call.Command] = m
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	want := map[string]map[string]any{
		"system.set_hostname":        {"hostname": "45-79-1-9.jabalihosted.com", "public_ipv4": "45.79.1.9"},
		"nginx.panel_landing_rehost": {"hostname": "45-79-1-9.jabalihosted.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent calls %v, want %v", got, want)
	}
}
