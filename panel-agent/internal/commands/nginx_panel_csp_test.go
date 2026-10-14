package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// The panel's Content-Security-Policy lives in an nginx snippet the agent
// rewrites from Server Settings → Support chat. With the chat off it must be
// exactly the CSP the vhost template carried inline before; with it on it
// allows support.jabali-panel.com for the widget's script, iframe and fetches,
// and nothing else changes.

// The CSP the panel vhost template sent inline before the snippet.
const panelCSPBefore = `default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https://gravatar.com https://secure.gravatar.com https://www.gravatar.com https://seccdn.libravatar.org; font-src 'self' data:; connect-src 'self' wss:; frame-ancestors 'none'; form-action 'self';`

var cspHeaderRE = regexp.MustCompile(`(?m)^add_header Content-Security-Policy "([^"]*)" always;$`)

func cspOf(t *testing.T, snippet string) string {
	t.Helper()
	m := cspHeaderRE.FindAllStringSubmatch(snippet, -1)
	if len(m) != 1 {
		t.Fatalf("want exactly one CSP add_header line, got %d:\n%s", len(m), snippet)
	}
	return m[0][1]
}

func cspDirectives(csp string) map[string]string {
	out := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		name, val, _ := strings.Cut(d, " ")
		out[name] = val
	}
	return out
}

func TestPanelCSP_OffIsTheCSPTheVhostHadInline(t *testing.T) {
	if got := cspOf(t, BuildPanelCSPSnippet(false)); got != panelCSPBefore {
		t.Fatalf("chat off must keep the panel's CSP unchanged\n got: %s\nwant: %s", got, panelCSPBefore)
	}
}

func TestPanelCSP_OnAllowsOnlyTheSupportOriginForTheWidget(t *testing.T) {
	on := cspDirectives(cspOf(t, BuildPanelCSPSnippet(true)))
	off := cspDirectives(panelCSPBefore)
	const origin = "https://support.jabali-panel.com"

	want := map[string]string{
		"script-src":  off["script-src"] + " " + origin,
		"connect-src": off["connect-src"] + " " + origin,
		"frame-src":   "'self' " + origin,
	}
	for name, val := range want {
		if on[name] != val {
			t.Errorf("%s: got %q want %q", name, on[name], val)
		}
	}
	for name, val := range off {
		if _, changed := want[name]; changed {
			continue
		}
		if on[name] != val {
			t.Errorf("%s must not change with the chat on: got %q want %q", name, on[name], val)
		}
	}
	if len(on) != len(off)+1 {
		t.Errorf("only frame-src may be added; got directives %v", on)
	}
}

// install.sh seeds the snippet on a host that has none yet (the vhost include
// must resolve before the agent ever runs). Its copy must be the agent's off
// rendering byte for byte, or a fresh install would serve another CSP until
// the panel's first start re-syncs it.
func TestPanelCSP_InstallSeedMatchesTheAgent(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	m := regexp.MustCompile(`(?s)<<'PANEL_CSP_OFF'\n(.*?)\nPANEL_CSP_OFF\n`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("install.sh has no PANEL_CSP_OFF heredoc seeding the panel CSP snippet")
	}
	if got, want := string(m[1])+"\n", BuildPanelCSPSnippet(false); got != want {
		t.Fatalf("install.sh's seed differs from the agent's off snippet\n got: %q\nwant: %q", got, want)
	}

	tmpl, err := os.ReadFile(filepath.Join(root, "install", "nginx", "jabali-panel-vhost.conf.tmpl"))
	if err != nil {
		t.Fatalf("read vhost template: %v", err)
	}
	server := string(tmpl)
	if loc := regexp.MustCompile(`(?m)^\s*location\s`).FindStringIndex(server); loc != nil {
		server = server[:loc[0]] // the server-level headers come before the first location
	}
	if !strings.Contains(server, "include /etc/nginx/snippets/jabali-panel-csp.conf;") {
		t.Error("the panel vhost must take its CSP from the snippet")
	}
	if strings.Contains(server, "Content-Security-Policy") {
		t.Error("the panel vhost must not also send a CSP inline: two CSP headers are both enforced")
	}
}

// cspExecFake answers nginx -t and systemctl reload, recording the calls.
type cspExecFake struct {
	mu        sync.Mutex
	calls     []string
	nginxTest string // shell script run for `nginx -t`
}

func (f *cspExecFake) install(t *testing.T) {
	t.Helper()
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		f.mu.Lock()
		f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
		f.mu.Unlock()
		script := "true"
		if name == "nginx" {
			script = f.nginxTest
		}
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
	t.Cleanup(func() { execCommandContext = prev })
}

func useTempCSPPath(t *testing.T) string {
	t.Helper()
	prev := panelCSPSnippetPath
	panelCSPSnippetPath = filepath.Join(t.TempDir(), "jabali-panel-csp.conf")
	t.Cleanup(func() { panelCSPSnippetPath = prev })
	return panelCSPSnippetPath
}

func applyPanelCSP(t *testing.T, params any) (*panelCSPApplyResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := panelCSPApplyHandler(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	resp, ok := res.(*panelCSPApplyResponse)
	if !ok {
		t.Fatalf("answer: got %T", res)
	}
	return resp, nil
}

func TestPanelCSPApply_WritesTestsAndReloads(t *testing.T) {
	path := useTempCSPPath(t)
	if err := os.WriteFile(path, []byte(BuildPanelCSPSnippet(false)), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &cspExecFake{nginxTest: "true"}
	f.install(t)

	resp, err := applyPanelCSP(t, map[string]any{"support_chat": true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.NoChange {
		t.Fatal("turning the chat on must change the snippet")
	}
	got, _ := os.ReadFile(path)
	if string(got) != BuildPanelCSPSnippet(true) {
		t.Fatalf("snippet on disk:\n%s", got)
	}
	if strings.Join(f.calls, "|") != "nginx -t|systemctl reload nginx" {
		t.Fatalf("want nginx -t then a reload, got %q", f.calls)
	}

	// The same state again touches nothing (the panel re-syncs on every start).
	f.calls = nil
	resp, err = applyPanelCSP(t, map[string]any{"support_chat": true})
	if err != nil || !resp.NoChange {
		t.Fatalf("an unchanged state must be a no-op; resp=%+v err=%v", resp, err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("a no-op must not test or reload nginx: %q", f.calls)
	}
}

func TestPanelCSPApply_RollsBackWhenNginxRejectsTheConfig(t *testing.T) {
	path := useTempCSPPath(t)
	before := BuildPanelCSPSnippet(false)
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &cspExecFake{nginxTest: "echo 'nginx: [emerg] boom' >&2; exit 1"}
	f.install(t)

	if _, err := applyPanelCSP(t, map[string]any{"support_chat": true}); err == nil {
		t.Fatal("a failed nginx -t must fail the apply")
	}
	got, _ := os.ReadFile(path)
	if string(got) != before {
		t.Fatalf("the snippet must be restored after a failed nginx -t:\n%s", got)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "systemctl reload") {
			t.Fatalf("nginx must not be reloaded after a failed test: %q", f.calls)
		}
	}
}

func TestPanelCSPApply_RefusesAMissingOrNonBoolState(t *testing.T) {
	useTempCSPPath(t)
	f := &cspExecFake{nginxTest: "true"}
	f.install(t)
	for _, params := range []any{map[string]any{}, map[string]any{"support_chat": "yes"}} {
		_, err := applyPanelCSP(t, params)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("params %v: want invalid_argument, got %v", params, err)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("nothing may run for bad params: %q", f.calls)
	}
}
