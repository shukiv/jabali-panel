package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// GH #2237: when a site is left, `jabali domain prune-orphans --apply --json`
// exits 1 with its JSON on stdout and "Error: …" on stderr. The handler hands
// the Repair Center that JSON, failed list and all.
func TestDomainRepairOrphans_ApplyFailureKeepsTheJSONResult(t *testing.T) {
	const stdout = `{"applied":true,"orphans":["a.example","b.example"],"deleted":["a.example"],"failed":[{"site":"b.example","error":"still in /etc/nginx/sites-enabled after the delete"}]}`
	var args []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, a ...string) *exec.Cmd {
		args = append([]string{name}, a...)
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' "$1"; echo 'Error: 1 orphan deletion(s) failed' >&2; exit 1`, "sh", stdout)
	}
	t.Cleanup(func() { execCommandContext = prev })

	out, err := domainRepairOrphansHandler(context.Background(), json.RawMessage(`{"apply":true}`))
	if err != nil {
		t.Fatalf("err = %v, want the CLI's JSON result", err)
	}
	if got := strings.Join(args, " "); got != repairBinary+" domain prune-orphans --apply --json" {
		t.Fatalf("ran %q", got)
	}
	res := out.(map[string]any)
	failed, _ := res["failed"].([]any)
	if len(failed) != 1 || failed[0].(map[string]any)["site"] != "b.example" {
		t.Fatalf("failed = %v, want b.example", res["failed"])
	}
}

// Output that is not JSON is an error carrying what the CLI printed.
func TestDomainRepairOrphans_UnparseableOutputIsAnError(t *testing.T) {
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, a ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `echo 'Error: agent domain.list: dial agent' >&2; exit 1`)
	}
	t.Cleanup(func() { execCommandContext = prev })

	_, err := domainRepairOrphansHandler(context.Background(), json.RawMessage(`{"apply":true}`))
	if err == nil || !strings.Contains(err.Error(), "dial agent") {
		t.Fatalf("err = %v, want the CLI's stderr in it", err)
	}
}
