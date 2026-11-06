package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

func TestLinkStaysIn(t *testing.T) {
	nginx := []string{"/etc/nginx", "/usr/share/nginx"}
	le := []string{"/etc/letsencrypt"}
	for _, tc := range []struct {
		live, dest string
		roots      []string
		ok         bool
	}{
		{"/etc/nginx/sites-enabled/a.conf", "/etc/nginx/sites-available/a.conf", nginx, true},
		{"/etc/nginx/modules-enabled/50-mod.conf", "/usr/share/nginx/modules-available/mod.conf", nginx, true},
		{"/etc/letsencrypt/live/a/cert.pem", "../../archive/a/cert1.pem", le, true},
		{"/etc/jabali-panel/sendmail/u/default.cred", "a.test.cred", []string{"/etc/jabali-panel"}, true},
		{"/etc/php/8.3/fpm/conf.d/10-opcache.ini", "/etc/php/8.3/mods-available/opcache.ini", []string{"/etc/php"}, true},
		{"/etc/nginx/x", "/etc/shadow", nginx, false},
		{"/etc/php/8.3/x", "/usr/share/nginx/modules-available/mod.conf", []string{"/etc/php"}, false},
		{"/etc/letsencrypt/live/a/cert.pem", "../../../shadow", le, false},
		{"/etc/letsencrypt/live/a/cert.pem", "../../../letsencrypt-other/x", le, false},
		{"/etc/nginx/sites-enabled/a", "d/../../x", nginx, false},
		{"/etc/nginx/sites-enabled/a", "/etc/nginx/../shadow", nginx, false},
		{"/etc/nginx/sites-enabled/a", "/", nginx, false},
		{"/etc/nginx/sites-enabled/a", "/etc/nginxx/a", nginx, false},
	} {
		if got := linkStaysIn(tc.live, tc.dest, tc.roots); got != tc.ok {
			t.Errorf("%s -> %s: got %v, want %v", tc.live, tc.dest, got, tc.ok)
		}
	}
}

// A staged link that would point outside the restore target is left out of
// the staged tree before rsync runs; the links a backup normally holds stay.
func TestRsyncStageOnto_LeavesOutLinksThatPointOutside(t *testing.T) {
	stage := t.TempDir()
	target := filepath.Join(t.TempDir(), "etc", "letsencrypt")
	src := filepath.Join(stage, target)
	for _, d := range []string{"live/a", "archive/a"} {
		if err := os.MkdirAll(filepath.Join(src, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "archive/a/cert1.pem"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"live/a/cert.pem":  "../../archive/a/cert1.pem",
		"live/a/abs.pem":   filepath.Join(target, "archive/a/cert1.pem"),
		"live/a/shadow":    "/etc/shadow",
		"live/a/climb.pem": "../../../../shadow",
	}
	for name, dest := range links {
		if err := os.Symlink(dest, filepath.Join(src, name)); err != nil {
			t.Fatal(err)
		}
	}
	var calls []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	warnings, err := rsyncStageOnto(context.Background(), stage, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, kept := range map[string]bool{"live/a/cert.pem": true, "live/a/abs.pem": true, "live/a/shadow": false, "live/a/climb.pem": false} {
		_, lerr := os.Lstat(filepath.Join(src, name))
		if (lerr == nil) != kept {
			t.Errorf("%s: kept=%v, want %v", name, lerr == nil, kept)
		}
	}
	if len(warnings) != 2 || !strings.Contains(strings.Join(warnings, "\n"), "/etc/shadow") {
		t.Errorf("warnings %q", warnings)
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "rsync ") {
		t.Errorf("calls %q", calls)
	}
}

// A system restore loads only the panel's own databases, and the name is
// never read as a client option.
func TestApplyPanelDBStage_LoadsOnlyThePanelDatabases(t *testing.T) {
	stage := t.TempDir()
	var calls []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	for _, db := range []string{"--init-command=SELECT 1", "mysql", "jabali_panel_x", "jabali_panel"} {
		if err := os.WriteFile(filepath.Join(stage, db+".sql"), []byte("SELECT 1;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, db := range []string{"--init-command=SELECT 1", "mysql", "jabali_panel_x"} {
		if err := applyPanelDBStage(context.Background(), stage, backup.ManifestStage{Name: backup.StagePanelDB, Items: []string{db}}); err == nil {
			t.Errorf("%q: loaded", db)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("a refused database must run nothing, ran %q", calls)
	}
	if err := applyPanelDBStage(context.Background(), stage, backup.ManifestStage{Name: backup.StagePanelDB, Items: []string{"jabali_panel"}}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.HasSuffix(calls[0], " -- jabali_panel") {
		t.Fatalf("calls %q", calls)
	}
}
