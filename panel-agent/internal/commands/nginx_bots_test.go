package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/useragents"
)

func TestBuildNginxBotsFragment_MapsAndEscaping(t *testing.T) {
	got := BuildNginxBotsFragment([]string{"Go-http-client/1.1", "go-http-client/1.1", "python-requests", "my+bot"})
	for _, want := range []string{
		"map $http_user_agent $jabali_ai_crawler {\n    default 0;\n",
		`    "~*GPTBot" 1;`,
		`    "~*ClaudeBot" 1;`,
		"map $http_user_agent $jabali_blocked_ua {\n    default 0;\n",
		// Regex metacharacters are escaped, so an entry matches literally.
		`    "~*Go-http-client/1\.1" 1;`,
		`    "~*my\+bot" 1;`,
		`    "~*python-requests" 1;`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fragment lacks %q:\n%s", want, got)
		}
	}
	// Case-insensitive duplicates collapse to one line.
	if n := strings.Count(strings.ToLower(got), `"~*go-http-client/1\.1"`); n != 1 {
		t.Errorf("Go-http-client listed %d times, want 1", n)
	}
	if again := BuildNginxBotsFragment([]string{"python-requests", "my+bot", "Go-http-client/1.1"}); again != got {
		t.Error("the same list in another order renders different bytes")
	}
	// Google-Extended is a robots.txt token, never a User-Agent.
	if strings.Contains(got, "Google-Extended") {
		t.Error("robots-only token Google-Extended is in the user-agent map")
	}
}

func TestBuildNginxBotsFragment_EmptyListStillDeclaresVariable(t *testing.T) {
	got := BuildNginxBotsFragment(nil)
	if !strings.Contains(got, "map $http_user_agent $jabali_blocked_ua {\n    default 0;\n}\n") {
		t.Fatalf("empty list must still declare $jabali_blocked_ua:\n%s", got)
	}
}

func TestBuildBotDirectives(t *testing.T) {
	if got := BuildBotDirectives(false, false); got != "" {
		t.Fatalf("nothing blocked must render nothing, got %q", got)
	}
	ai := BuildBotDirectives(true, false)
	if !strings.Contains(ai, "if ($jabali_ai_crawler) { return 403; }") || strings.Contains(ai, "jabali_blocked_ua") {
		t.Fatalf("AI only: %q", ai)
	}
	both := BuildBotDirectives(true, true)
	if !strings.Contains(both, "if ($jabali_blocked_ua) { return 403; }") {
		t.Fatalf("both: %q", both)
	}
}

// A vhost that blocks something gets the lines and a fragment to resolve the
// variables against; one that blocks nothing gets neither.
func TestLimitDirectives_WritesFragmentOnlyWhenNeeded(t *testing.T) {
	frag := filepath.Join(t.TempDir(), "00-jabali-bots.conf")
	prev := nginxBotsFragment
	nginxBotsFragment = frag
	t.Cleanup(func() { nginxBotsFragment = prev })

	got, err := limitDirectives(domainCreateParams{})
	if err != nil || got != "" {
		t.Fatalf("no limits: %q, %v", got, err)
	}
	if _, err := os.Stat(frag); err == nil {
		t.Fatal("fragment written for a vhost that blocks nothing")
	}

	got, err = limitDirectives(domainCreateParams{BlockAICrawlers: true})
	if err != nil || !strings.Contains(got, "$jabali_ai_crawler") {
		t.Fatalf("AI block: %q, %v", got, err)
	}
	body, err := os.ReadFile(frag)
	if err != nil || !strings.Contains(string(body), "$jabali_blocked_ua") {
		t.Fatalf("fragment not written: %v\n%s", err, body)
	}
}

func botsApplyStub(t *testing.T, nginxTestFails bool) (frag string, calls *[]string) {
	t.Helper()
	frag = filepath.Join(t.TempDir(), "00-jabali-bots.conf")
	prevPath := nginxBotsFragment
	nginxBotsFragment = frag
	var seen []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		seen = append(seen, name+" "+strings.Join(args, " "))
		if name == "nginx" && nginxTestFails {
			return exec.CommandContext(ctx, "sh", "-c", "echo 'unknown directive' >&2; exit 1")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() {
		execCommandContext = prev
		nginxBotsFragment = prevPath
	})
	return frag, &seen
}

func applyBots(t *testing.T, list []string) (any, error) {
	t.Helper()
	params, _ := json.Marshal(nginxBotsApplyParams{BlockedUserAgents: list})
	return nginxBotsApplyHandler(context.Background(), params)
}

func TestNginxBotsApply_WritesTestsAndReloads(t *testing.T) {
	frag, calls := botsApplyStub(t, false)
	if _, err := applyBots(t, []string{"python-requests"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(frag)
	if !strings.Contains(string(body), `"~*python-requests" 1;`) {
		t.Fatalf("fragment:\n%s", body)
	}
	if strings.Join(*calls, "|") != "nginx -t|systemctl reload nginx" {
		t.Fatalf("commands = %v, want nginx -t then reload", *calls)
	}
	// Same list again: nothing to do, no reload.
	*calls = nil
	got, err := applyBots(t, []string{"python-requests"})
	if err != nil || !got.(*nginxBotsApplyResponse).NoChange || len(*calls) != 0 {
		t.Fatalf("re-apply: %+v %v, commands %v", got, err, *calls)
	}
}

func TestNginxBotsApply_FailedTestKeepsPreviousFile(t *testing.T) {
	frag, calls := botsApplyStub(t, true)
	old := BuildNginxBotsFragment([]string{"old-bot"})
	if err := os.WriteFile(frag, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := applyBots(t, []string{"new-bot"}); err == nil {
		t.Fatal("nginx -t failed but apply succeeded")
	}
	body, _ := os.ReadFile(frag)
	if string(body) != old {
		t.Fatalf("previous fragment not restored:\n%s", body)
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "systemctl reload") {
			t.Fatal("reloaded nginx after a failed test")
		}
	}
	if _, err := os.Stat(frag + ".bak"); err == nil {
		t.Fatal("backup left behind")
	}
}

func TestNginxBotsApply_RefusesBadInput(t *testing.T) {
	frag, calls := botsApplyStub(t, false)
	if _, err := applyBots(t, []string{"ok-bot", `x" 1; } server { listen 1; #`}); err == nil {
		t.Fatal("an entry that would break out of the map was accepted")
	}
	tooMany := make([]string, useragents.MaxBlocked+1)
	for i := range tooMany {
		tooMany[i] = "bot-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := applyBots(t, tooMany); err == nil {
		t.Fatalf("%d entries accepted, cap is %d", len(tooMany), useragents.MaxBlocked)
	}
	if _, err := os.Stat(frag); err == nil || len(*calls) != 0 {
		t.Fatal("refused input still wrote the fragment or ran nginx")
	}
}

// No AI token may match a Google or Bing crawler: the "Block AI crawlers"
// switch must never block Google or Bing.
func TestAICrawlerList_NeverMatchesProtectedClients(t *testing.T) {
	for _, tok := range aiCrawlerUserAgents {
		if name := useragents.Protected(tok); name != "" {
			t.Errorf("AI token %q would also block %s", tok, name)
		}
	}
}
