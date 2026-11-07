package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// withSnippetSeams points the per-domain snippet dir at a temp dir and records
// the commands run instead of running them (each "succeeds" via true(1)).
func withSnippetSeams(t *testing.T) func() []string {
	t.Helper()
	origDir, origExec := nginxJabaliDir, execCommandContext
	nginxJabaliDir = t.TempDir()
	var mu sync.Mutex
	var cmds []string
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		mu.Lock()
		cmds = append(cmds, strings.Join(append([]string{name}, args...), " "))
		mu.Unlock()
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { nginxJabaliDir, execCommandContext = origDir, origExec })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), cmds...)
	}
}

// Every app snippet writer changes nginx config and runs nginx -t + reload, so
// it must hold nginxOpMu across the whole sequence (JAB-71): while another
// nginx op holds the lock, a writer may not finish.
func TestAppSnippetWritersWaitForTheNginxLock(t *testing.T) {
	withSnippetSeams(t)
	ctx := context.Background()
	const dom = "shop.example.com"
	writers := map[string]func() error{
		"invoiceshelf write": func() error {
			return writeInvoiceShelfNginx(ctx, dom, "alice", "", "/home/alice/domains/shop.example.com/public_html")
		},
		"invoiceshelf remove": func() error { return removeInvoiceShelfNginx(ctx, dom, "") },
		"nextcloud write":     func() error { return writeNextcloudNginx(ctx, dom, "alice", "/run/php/jabali-alice/fpm.sock") },
		"nextcloud remove":    func() error { return removeNextcloudNginx(ctx, dom) },
		"openemr write":       func() error { return writeOpenEMRNginx(ctx, dom, "alice", "") },
		"openemr remove":      func() error { return removeOpenEMRNginx(ctx, dom, "") },
		"flarum write":        func() error { return writeFlarumNginx(ctx, dom, "alice", "") },
		"flarum remove":       func() error { return removeFlarumNginx(ctx, dom, "") },
		"osticket write":      func() error { return writeOsTicketNginx(ctx, dom, "alice") },
		"osticket remove":     func() error { return removeOsTicketNginx(ctx, dom) },
		"xmlrpc block write":  func() error { return writeWordPressXmlrpcBlock(ctx, dom) },
		"xmlrpc block remove": func() error { return removeWordPressXmlrpcBlock(ctx, dom) },
		"app rewrite write":   func() error { return writeAppRewrite(ctx, "drupal", dom, "alice", "blog") },
		"app rewrite remove":  func() error { return removeAppRewrite(ctx, "drupal", dom, "blog") },
	}
	for name, write := range writers {
		nginxOpMu.Lock()
		done := make(chan error, 1)
		go func() { done <- write() }()
		select {
		case err := <-done:
			nginxOpMu.Unlock()
			t.Fatalf("%s finished (err %v) while another nginx op held the lock", name, err)
		case <-time.After(100 * time.Millisecond):
		}
		nginxOpMu.Unlock()
		if err := <-done; err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestInvoiceShelfNginx_RefusesAnInstallPathNginxReadsAsSyntax(t *testing.T) {
	ran := withSnippetSeams(t)
	for _, p := range []string{
		"/home/alice/domains/shop.example.com/public_html$x",
		"/home/alice/domains/shop.example.com/public_html#x",
		`/home/alice/domains/shop.example.com/public_html\x`,
		"/home/alice/domains/shop.example.com/public_html;x",
		"home/alice/domains/shop.example.com/public_html",
	} {
		if err := writeInvoiceShelfNginx(context.Background(), "shop.example.com", "alice", "", p); err == nil {
			t.Fatalf("install path %q was accepted", p)
		}
	}
	if cmds := ran(); len(cmds) != 0 {
		t.Fatalf("nginx was run for a refused snippet: %v", cmds)
	}
}

// The install refuses what the snippet cannot carry before anything runs as
// the user, instead of failing at the end with the app already installed.
func TestInvoiceShelfInstall_RefusesSnippetValuesBeforeAnyStep(t *testing.T) {
	base := map[string]any{
		"os_user":      "alice",
		"docroot":      "/home/alice/domains/shop.example.com/public_html",
		"subdirectory": "billing",
		"site_url":     "https://shop.example.com/billing",
		"admin_email":  "admin@example.com",
		"admin_pass":   "x",
		"db_name":      "alice_inv",
		"db_user":      "alice_inv",
		"db_password":  "x",
	}
	for name, change := range map[string][2]string{
		"comment in the docroot":  {"docroot", "/home/alice/domains/shop.example.com/public_html#x"},
		"variable in the docroot": {"docroot", "/home/alice/domains/shop.example.com/public_$x"},
		"not a subdir slug":       {"subdirectory", "Billing Dept"},
	} {
		ran := withSnippetSeams(t)
		params := map[string]any{}
		for k, v := range base {
			params[k] = v
		}
		params[change[0]] = change[1]
		raw, _ := json.Marshal(params)
		_, err := invoiceshelfInstallHandler(context.Background(), raw)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Fatalf("%s: want InvalidArgument, got %v", name, err)
		}
		if cmds := ran(); len(cmds) != 0 {
			t.Fatalf("%s: commands ran before the refusal: %v", name, cmds)
		}
	}
}
