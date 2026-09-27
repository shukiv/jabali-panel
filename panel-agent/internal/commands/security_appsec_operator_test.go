package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

// operatorApplyEnv points the verb at a temp crowdsec tree and records every
// systemctl call. failReload/failRestart make that step exit non-zero.
func operatorApplyEnv(t *testing.T, failReload, failRestart bool) (path string, calls *[]string) {
	t.Helper()
	dataDir := t.TempDir()
	prevPath, prevDir, prevExec := appsecOperatorBeforePath, appsecCrowdsecDataDir, execCommandContext
	appsecCrowdsecDataDir = dataDir
	appsecOperatorBeforePath = filepath.Join(dataDir, "crs-plugins", "jabali", "jabali-operator-before.conf")
	var rec []string
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		call := strings.Join(append([]string{name}, args...), " ")
		rec = append(rec, call)
		if (failReload && call == "systemctl reload crowdsec") || (failRestart && call == "systemctl restart crowdsec") {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() {
		appsecOperatorBeforePath, appsecCrowdsecDataDir, execCommandContext = prevPath, prevDir, prevExec
	})
	return appsecOperatorBeforePath, &rec
}

func flarumRow() appseccfg.Exclusion {
	return appseccfg.Exclusion{Host: "forum.example.com", URIPrefix: "/forum/api/", RuleID: "920450", Note: "Flarum"}
}

func callOperatorApply(t *testing.T, p any) (appseccfg.OperatorApplyResult, error) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := appsecOperatorApplyHandler(context.Background(), raw)
	if err != nil {
		return appseccfg.OperatorApplyResult{}, err
	}
	return out.(appseccfg.OperatorApplyResult), nil
}

// The file the verb writes must be exactly what `jabali appsec render-config`
// writes for the same rows — both go through RenderOperatorBeforeFile — or the
// two writers would rewrite each other's output and reload crowdsec for nothing.
func TestOperatorApply_WritesRenderedFileAndReloads(t *testing.T) {
	path, calls := operatorApplyEnv(t, false, false)
	p := appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{flarumRow()}, nil)

	res, err := callOperatorApply(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || !res.Reloaded {
		t.Fatalf("first apply: want changed+reloaded, got %+v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := appseccfg.RenderOperatorBeforeFile(p.Exclusions, p.HostModes); string(got) != want {
		t.Fatalf("file differs from the shared renderer:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(string(got), "ctl:ruleRemoveById=920450") {
		t.Fatalf("exclusion not live in the file:\n%s", got)
	}
	if len(*calls) != 1 || (*calls)[0] != "systemctl reload crowdsec" {
		t.Fatalf("want exactly one reload, got %v", *calls)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("want mode 0644, got %v (%v)", fi.Mode().Perm(), err)
	}

	// Same rows again: nothing to write, so no reload.
	*calls = nil
	res, err = callOperatorApply(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || res.Reloaded || len(*calls) != 0 {
		t.Fatalf("unchanged apply must be a no-op, got %+v calls=%v", res, *calls)
	}
}

// An empty desired state removes the file, so a since-removed exclusion cannot
// stay live. An already-absent file is not a change.
func TestOperatorApply_EmptySetRemovesFile(t *testing.T) {
	path, calls := operatorApplyEnv(t, false, false)
	if _, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{flarumRow()}, nil)); err != nil {
		t.Fatal(err)
	}
	*calls = nil

	res, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || !res.Reloaded {
		t.Fatalf("removal: want changed+reloaded, got %+v", res)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("operator file still present after an empty apply (err=%v)", err)
	}

	*calls = nil
	res, err = callOperatorApply(t, appseccfg.NewOperatorApplyParams(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || len(*calls) != 0 {
		t.Fatalf("absent file + empty set must be a no-op, got %+v calls=%v", res, *calls)
	}
}

// A request missing a list is refused and leaves the live file alone: reading
// it as "none" would drop every live entry of that kind.
func TestOperatorApply_MissingListRefusedFileUntouched(t *testing.T) {
	path, calls := operatorApplyEnv(t, false, false)
	if _, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{flarumRow()}, nil)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	*calls = nil

	_, err := callOperatorApply(t, map[string]any{"exclusions": []any{}})
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if len(before) == 0 || string(after) != string(before) || len(*calls) != 0 {
		t.Fatalf("refused request changed the host: calls=%v", *calls)
	}
}

// An invalid row renders as a comment, as it does from the CLI, and the valid
// rows still go live. Its fields cannot escape the comment.
func TestOperatorApply_InvalidRowSkippedNotInjected(t *testing.T) {
	path, _ := operatorApplyEnv(t, false, false)
	bad := flarumRow()
	bad.Host = "x.example.com\nSecRule REQUEST_URI \"@beginsWith /\" \"id:9596999,phase:1,pass,ctl:ruleRemoveById=949110\""
	if _, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{flarumRow(), bad}, nil)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "# SKIPPED") || !strings.Contains(string(got), "ctl:ruleRemoveById=920450") {
		t.Fatalf("want the bad row skipped and the good row live:\n%s", got)
	}
	for _, line := range strings.Split(string(got), "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "SecRule") && strings.Contains(l, "949110") {
			t.Fatalf("injected directive is live: %q", l)
		}
	}
}

func TestOperatorApply_NoCrowdsecIsSkipped(t *testing.T) {
	path, calls := operatorApplyEnv(t, false, false)
	appsecCrowdsecDataDir = filepath.Join(t.TempDir(), "absent")
	res, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{flarumRow()}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped == "" || res.Changed {
		t.Fatalf("want skipped, got %+v", res)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || len(*calls) != 0 {
		t.Fatalf("wrote or exec'd without crowdsec: err=%v calls=%v", err, *calls)
	}
}

// A failed reload falls back to restart. Only when both fail is it an error,
// so the panel logs it instead of assuming the exclusion is live.
func TestOperatorApply_ReloadFallbackAndFailure(t *testing.T) {
	t.Run("restart rescues a failed reload", func(t *testing.T) {
		_, calls := operatorApplyEnv(t, true, false)
		res, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{flarumRow()}, nil))
		if err != nil || !res.Reloaded {
			t.Fatalf("want reloaded via restart, got %+v err=%v", res, err)
		}
		if strings.Join(*calls, ";") != "systemctl reload crowdsec;systemctl restart crowdsec" {
			t.Fatalf("unexpected calls %v", *calls)
		}
	})
	t.Run("both fail", func(t *testing.T) {
		_, _ = operatorApplyEnv(t, true, true)
		_, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{flarumRow()}, nil))
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInternal {
			t.Fatalf("want Internal, got %v", err)
		}
	})
}
