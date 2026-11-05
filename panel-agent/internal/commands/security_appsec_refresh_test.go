package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

// GH #2380: security.crowdsec.appsec.refresh puts the panel's current webmail
// host list into jabali-appsec.yaml and keeps everything else as the live file
// has it.

// refreshEnv is operatorApplyEnvExec plus a temp webmail host list holding
// hosts. It returns the list's path and the exec'd commands.
func refreshEnv(t *testing.T, fail map[string]bool, hosts ...string) (string, *[]string) {
	t.Helper()
	_, calls := operatorApplyEnvExec(t, fail, nil)
	listPath := filepath.Join(t.TempDir(), "webmail-hosts.list")
	prev := appsecWebmailHostsPath
	appsecWebmailHostsPath = listPath
	t.Cleanup(func() { appsecWebmailHostsPath = prev })
	if _, err := appseccfg.WriteWebmailHosts(listPath, hosts); err != nil {
		t.Fatal(err)
	}
	return listPath, calls
}

// refreshOpts is jabali-appsec.yaml's settings as a writer left them: geoblock
// on, bot detection balanced, one exclusion by rule name. panelHost "" is the
// path-only form `jabali appsec render-config` used to write.
func refreshOpts(panelHost string, webmail ...string) appseccfg.Opts {
	return appseccfg.Opts{
		Mode:           "deny",
		Countries:      []string{"KP", "RU"},
		Inband:         appseccfg.DetectInbandRules(appsecRulesDir),
		AdminAllowlist: true,
		PanelHost:      panelHost,
		WebmailHosts:   webmail,
		BotDetection:   "balanced",
		BotScope:       "all",
		RuleExclusions: []appseccfg.Exclusion{{Host: "shop.example.com", URIPrefix: "/api/", RuleID: "crowdsecurity/vpatch-CVE-2023-22515"}},
	}
}

// liveConfig writes the config rendered from refreshOpts and returns it.
func liveConfig(t *testing.T, panelHost string, webmail ...string) string {
	t.Helper()
	body := appseccfg.Render(refreshOpts(panelHost, webmail...))
	if err := os.WriteFile(appsecRulePath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return body
}

func callRefresh(t *testing.T) (bool, error) {
	t.Helper()
	out, err := csAppSecRefreshHandler(context.Background(), nil)
	if err != nil {
		return false, err
	}
	return out.(map[string]any)["changed"].(bool), nil
}

func readAppsec(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(appsecRulePath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A domain that gained mail since the config was rendered gets on the list,
// and nothing else in the file moves.
func TestAppSecRefresh_AddsANewWebmailHost(t *testing.T) {
	for _, panelHost := range []string{"", "panel.example.com"} {
		t.Run("panel host "+panelHost, func(t *testing.T) {
			_, calls := refreshEnv(t, nil, "mail.old.example", "mail.new.example")
			liveConfig(t, panelHost, "mail.old.example")
			// A path-only allowlist is scoped to this host on the way.
			wantHost := panelHost
			if wantHost == "" {
				wantHost = appsecPanelHost()
			}
			want := appseccfg.Render(refreshOpts(wantHost, "mail.old.example", "mail.new.example"))

			changed, err := callRefresh(t)
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("changed = false, want true")
			}
			got := readAppsec(t)
			if got != want {
				t.Fatalf("config is not the live settings with the new list:\n--- got\n%s\n--- want\n%s", got, want)
			}
			if !strings.Contains(got, `req.Host == "mail.new.example"`) {
				t.Fatal("the new host is not on the allowlist")
			}
			if got := strings.Join(*calls, "; "); got != "crowdsec -t; systemctl reload crowdsec" {
				t.Fatalf("calls = %q, want the check then the reload", got)
			}
		})
	}
}

// A config already rendered from the list, by the CLI or by the agent, is
// left alone: no write, no reload. Otherwise every audit pass would reload
// crowdsec. Both writers scope the panel allowlist to this host; one scoped to
// another host (renamed since) keeps it.
func TestAppSecRefresh_CurrentConfigIsLeftAlone(t *testing.T) {
	for _, panelHost := range []string{appsecPanelHost(), "panel.example.com"} {
		t.Run("panel host "+panelHost, func(t *testing.T) {
			_, calls := refreshEnv(t, nil, "mail.a.example", "autoconfig.a.example")
			before := liveConfig(t, panelHost, "mail.a.example", "autoconfig.a.example")

			changed, err := callRefresh(t)
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				t.Fatal("changed = true for a current config")
			}
			if readAppsec(t) != before {
				t.Fatal("a current config was rewritten")
			}
			if len(*calls) != 0 {
				t.Fatalf("calls = %q, want none", *calls)
			}
		})
	}
}

// A path-only panel allowlist, as `jabali appsec render-config` used to write
// it, is scoped to this host by the next refresh even when the webmail list
// has not changed: the config is then the one both writers render.
func TestAppSecRefresh_ScopesAPathOnlyPanelAllowlist(t *testing.T) {
	host := appsecPanelHost()
	if host == "" {
		t.Skip("no hostname")
	}
	_, calls := refreshEnv(t, nil, "mail.a.example")
	if live := liveConfig(t, "", "mail.a.example"); !strings.Contains(live, " - filter: req.URL.Path startsWith \"/api/v1/\"") {
		t.Fatalf("the live config is not path-only to begin with:\n%s", live)
	}

	changed, err := callRefresh(t)
	if err != nil {
		t.Fatal(err)
	}
	got := readAppsec(t)
	if want := appseccfg.Render(refreshOpts(host, "mail.a.example")); !changed || got != want {
		t.Fatalf("changed = %v\n--- got\n%s\n--- want\n%s", changed, got, want)
	}
	if !strings.Contains(got, `/dav/") && req.Host in ["`+host+`", "`+host+`:8443"]`) {
		t.Fatalf("the panel allowlist is not scoped to this host:\n%s", got)
	}
	if got := strings.Join(*calls, "; "); got != "crowdsec -t; systemctl reload crowdsec" {
		t.Fatalf("calls = %q, want the check then the reload", got)
	}
}

// Without jabali-appsec.yaml, AppSec is not set up: nothing is created and
// crowdsec is not touched.
func TestAppSecRefresh_NoConfigDoesNothing(t *testing.T) {
	_, calls := refreshEnv(t, nil, "mail.a.example")

	changed, err := callRefresh(t)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("changed = true without a config")
	}
	if _, err := os.Stat(appsecRulePath); !os.IsNotExist(err) {
		t.Fatalf("a config was created: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("calls = %q, want none", *calls)
	}
}

// A config crowdsec refuses is put back, and crowdsec is not reloaded.
func TestAppSecRefresh_RefusedConfigIsPutBack(t *testing.T) {
	_, calls := refreshEnv(t, map[string]bool{"crowdsec -t": true}, "mail.a.example", "mail.b.example")
	before := liveConfig(t, "", "mail.a.example")

	if _, err := callRefresh(t); err == nil {
		t.Fatal("no error for a config crowdsec -t refused")
	}
	if readAppsec(t) != before {
		t.Fatal("the refused config was left in place")
	}
	if got := strings.Join(*calls, "; "); got != "crowdsec -t" {
		t.Fatalf("calls = %q, want the check only", got)
	}
}

// A refresh waits for another writer of the AppSec configs, so it cannot
// render from a header that writer is replacing or put back what it wrote.
func TestAppSecRefresh_WaitsForAnotherWriter(t *testing.T) {
	refreshEnv(t, nil, "mail.a.example", "mail.b.example")
	liveConfig(t, "", "mail.a.example")

	appsecConfigMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := callRefreshErr()
		done <- err
	}()
	select {
	case <-done:
		appsecConfigMu.Unlock()
		t.Fatal("the refresh ran while another writer held the AppSec configs")
	case <-time.After(200 * time.Millisecond):
	}
	appsecConfigMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh did not run once the other writer finished")
	}
	if !strings.Contains(readAppsec(t), `req.Host == "mail.b.example"`) {
		t.Fatal("the refresh did not write the list after waiting")
	}
}

func callRefreshErr() (any, error) {
	return csAppSecRefreshHandler(context.Background(), nil)
}
