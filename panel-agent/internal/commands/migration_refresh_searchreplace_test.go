package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestRefreshReconcile_SearchReplaceArgs pins the wp search-replace invocation
// for migration.refresh_reconcile: the old/new URLs are the trailing positionals
// AFTER the flags, and there is NO "--" separator. wp-cli 2.12 mis-parses a "--"
// token alongside --all-tables (it treats the replacement URL as a table filter
// and fails with "Couldn't find any tables matching: <new_url>"), so a
// regression here would silently break every site-URL rewrite (GH #1579 / #646).
func TestRefreshReconcile_SearchReplaceArgs(t *testing.T) {
	var srArgs []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if strings.Contains(strings.Join(args, " "), "search-replace") {
			srArgs = append([]string{name}, args...)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	raw, _ := json.Marshal(refreshReconcileParams{
		OSUser:      "u1",
		InstallPath: "/home/u1/public_html/site",
		Domain:      "new.com",
		OldURL:      "https://old.com",
		NewURL:      "https://new.com",
	})
	if _, err := migrationRefreshReconcileHandler(context.Background(), raw); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if srArgs == nil {
		t.Fatal("search-replace was never invoked")
	}

	for _, a := range srArgs {
		if a == "--" {
			t.Fatalf(`search-replace must not use a "--" separator (breaks wp-cli 2.12): %v`, srArgs)
		}
	}

	// The replacement URL must appear AFTER the flags (as the <new> positional),
	// not be consumed as a table filter.
	joined := strings.Join(srArgs, " ")
	iFlag := strings.Index(joined, "--skip-columns=guid")
	iOld := strings.Index(joined, "https://old.com")
	iNew := strings.Index(joined, "https://new.com")
	if iFlag < 0 || iOld < 0 || iNew < 0 {
		t.Fatalf("expected flags + both URLs in args: %v", srArgs)
	}
	if !(iFlag < iOld && iOld < iNew) {
		t.Fatalf("expected order flags < old < new, got flag=%d old=%d new=%d (%v)", iFlag, iOld, iNew, srArgs)
	}
}
