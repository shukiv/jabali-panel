package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/settingsops"
)

// nginx_tunables_boot_test.go — the panel re-applies Server Settings → Nginx
// at start, so the live nginx limits match what the page shows. Until now only
// a save wrote /etc/nginx/conf.d/05-jabali-tunables.conf: a box installed
// before that file existed never got one (nginx's built-in 1m body limit,
// while the page said 512m), and a box seeded with the old 50m default kept
// 50m after migration 000252 moved the setting to 512m.

type tunablesCall struct {
	command string
	params  map[string]any
}

type tunablesAgent struct {
	calls []tunablesCall
	resp  json.RawMessage
	err   error
}

func (a *tunablesAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	b, _ := json.Marshal(params)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	a.calls = append(a.calls, tunablesCall{command, m})
	if a.err != nil {
		return nil, a.err
	}
	return a.resp, nil
}

func savedNginxSettings() *models.ServerSettings {
	return &models.ServerSettings{
		ID:                       1,
		NginxClientMaxBodySize:   "512m",
		NginxKeepaliveTimeout:    "65s",
		NginxServerTokens:        false,
		NginxGzip:                true,
		NginxClientBodyTimeout:   "61s",
		NginxClientHeaderTimeout: "62s",
		NginxSendTimeout:         "63s",
		NginxProxyConnectTimeout: "301s",
		NginxProxyReadTimeout:    "302s",
		NginxProxySendTimeout:    "303s",
		NginxWorkerProcesses:     "auto",
		NginxWorkerConnections:   768,
		NginxCustomHTTP:          "# admin",
	}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)) }

func TestResyncNginxTunables_AppliesTheSavedSettings(t *testing.T) {
	row := savedNginxSettings()
	a := &tunablesAgent{resp: json.RawMessage(`{"no_change":true}`)}
	resyncNginxTunables(context.Background(), a, row, false, discardLog())
	if len(a.calls) != 1 || a.calls[0].command != "nginx.tunables.apply" {
		t.Fatalf("agent calls = %+v, want one nginx.tunables.apply", a.calls)
	}
	got := a.calls[0].params
	if got["client_max_body_size"] != "512m" {
		t.Fatalf("client_max_body_size = %v, want the saved 512m", got["client_max_body_size"])
	}
	// The whole wire, from the one place a save builds it — a boot re-apply
	// that dropped a key would render that directive out of the fragment.
	b, _ := json.Marshal(settingsops.NginxTunablesParams(row))
	var want map[string]any
	_ = json.Unmarshal(b, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("params = %v\nwant %v", got, want)
	}
}

func TestResyncNginxTunables_SkipsWhenThereIsNothingSaved(t *testing.T) {
	empty := savedNginxSettings()
	empty.NginxClientMaxBodySize = ""
	for name, tc := range map[string]struct {
		row     *models.ServerSettings
		created bool
	}{
		// First boot: the row was just created with struct-zero fields.
		"a row created this boot": {savedNginxSettings(), true},
		// An empty size renders a fragment without client_max_body_size, which
		// puts nginx back on its 1m default.
		"no body size": {empty, false},
		"no row":       {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			a := &tunablesAgent{}
			resyncNginxTunables(context.Background(), a, tc.row, tc.created, discardLog())
			if len(a.calls) != 0 {
				t.Fatalf("agent called: %+v", a.calls)
			}
		})
	}
}

func TestResyncNginxTunables_AgentErrorIsLoggedNotFatal(t *testing.T) {
	var buf bytes.Buffer
	a := &tunablesAgent{err: errors.New("nginx -t failed")}
	resyncNginxTunables(context.Background(), a, savedNginxSettings(), false, slog.New(slog.NewTextHandler(&buf, nil)))
	if !strings.Contains(buf.String(), "nginx tunables re-apply failed") || !strings.Contains(buf.String(), "nginx -t failed") {
		t.Fatalf("log = %q, want the failure logged", buf.String())
	}
}

// The helper only helps if the panel start calls it.
func TestServeResyncsNginxTunablesAtStart(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "resyncNginxTunables(") {
		t.Fatal("serve.go never calls resyncNginxTunables: Server Settings → Nginx only reaches nginx on a save")
	}
}
