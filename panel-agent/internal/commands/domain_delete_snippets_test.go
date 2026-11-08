package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// withDomainDeleteSeams points the snippet dir and the certificate dirs at a
// temp dir and replaces every command with true(1). onReload, when set, runs
// when the handler reloads nginx.
func withDomainDeleteSeams(t *testing.T, onReload func()) string {
	t.Helper()
	tmp := t.TempDir()
	prevDir, prevExec := nginxJabaliDir, execCommandContext
	prevSelf, prevLE := baseSelfSignDir, sslLERoot
	nginxJabaliDir = filepath.Join(tmp, "jabali")
	baseSelfSignDir, sslLERoot = filepath.Join(tmp, "self"), filepath.Join(tmp, "le")
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if onReload != nil && name == "systemctl" && len(args) > 0 && args[0] == "reload" {
			onReload()
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() {
		nginxJabaliDir, execCommandContext = prevDir, prevExec
		baseSelfSignDir, sslLERoot = prevSelf, prevLE
	})
	return nginxJabaliDir
}

func writeSnippetFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("# snippet\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A domain delete never runs the apps' own teardown, and the vhost includes
// every *.conf in /etc/nginx/jabali/<domain>/. So the delete takes that dir with
// it, before the reload; another domain's dir stays.
func TestDomainDelete_ClearsTheDomainsSnippetDir(t *testing.T) {
	const dom, keep = "snipdel.test", "snipkeep.test"
	var root string
	presentAtReload := true
	root = withDomainDeleteSeams(t, func() {
		_, err := os.Lstat(filepath.Join(root, dom))
		presentAtReload = err == nil
	})
	writeSnippetFiles(t, filepath.Join(root, dom), "invoiceshelf-invoices.conf", "wordpress-xmlrpc.conf", "php-pins.params")
	writeSnippetFiles(t, filepath.Join(root, keep), "wordpress-xmlrpc.conf", "php-pins.params")

	params, _ := json.Marshal(domainDeleteParams{Domain: dom})
	if _, err := domainDeleteHandler(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, dom)); !os.IsNotExist(err) {
		t.Fatalf("the deleted domain's snippet dir was left behind (err=%v)", err)
	}
	if presentAtReload {
		t.Fatal("nginx was reloaded before the snippet dir was removed")
	}
	if entries, err := os.ReadDir(filepath.Join(root, keep)); err != nil || len(entries) != 2 {
		t.Fatalf("another domain's snippets were touched: %v %v", entries, err)
	}
}

// The delete removes nginx config and reloads, so it holds nginxOpMu across
// both (JAB-71): while another nginx op holds the lock, it may not finish.
func TestDomainDelete_WaitsForTheNginxLock(t *testing.T) {
	withDomainDeleteSeams(t, nil)
	params, _ := json.Marshal(domainDeleteParams{Domain: "snipdel.test"})

	nginxOpMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := domainDeleteHandler(context.Background(), params)
		done <- err
	}()
	select {
	case err := <-done:
		nginxOpMu.Unlock()
		t.Fatalf("domain.delete finished (err %v) while another nginx op held the lock", err)
	case <-time.After(100 * time.Millisecond):
	}
	nginxOpMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
