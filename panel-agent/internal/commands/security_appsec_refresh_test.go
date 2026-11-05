package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
// form `jabali appsec render-config` writes.
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
			want := appseccfg.Render(refreshOpts(panelHost, "mail.old.example", "mail.new.example"))

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
// crowdsec.
func TestAppSecRefresh_CurrentConfigIsLeftAlone(t *testing.T) {
	for _, panelHost := range []string{"", "panel.example.com"} {
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
