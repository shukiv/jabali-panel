package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A deleted domain's MTA-STS vhost (`<domain>-mta-sts.conf`) and policy dir go
// with it. Left behind, the vhost keeps pointing at the domain's certificate,
// which the teardown removes: from then on `nginx -t` fails for the whole
// server, so every later vhost write and reload fails, and nginx doesn't start.
func TestDomainDelete_ReapsTheMTAStsVhost(t *testing.T) {
	tmp := t.TempDir()
	avail, enabl, root := filepath.Join(tmp, "avail"), filepath.Join(tmp, "enabl"), filepath.Join(tmp, "mta-sts")
	for _, d := range []string{avail, enabl, root} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prevA, prevE, prevR := mtaStsSitesAvail, mtaStsSitesEnabl, mtaStsRoot
	prevSelf, prevLE := baseSelfSignDir, sslLERoot
	mtaStsSitesAvail, mtaStsSitesEnabl, mtaStsRoot = avail, enabl, root
	baseSelfSignDir, sslLERoot = filepath.Join(tmp, "self"), filepath.Join(tmp, "le")
	t.Cleanup(func() {
		mtaStsSitesAvail, mtaStsSitesEnabl, mtaStsRoot = prevA, prevE, prevR
		baseSelfSignDir, sslLERoot = prevSelf, prevLE
	})

	const dom, keep = "mtasts-delete.test", "mtasts-keep.test"
	for _, d := range []string{dom, keep} {
		conf := filepath.Join(avail, d+"-mta-sts.conf")
		if err := os.WriteFile(conf, []byte("server {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(conf, filepath.Join(enabl, d+"-mta-sts.conf")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, d, ".well-known"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The reload must come after the reap, so nginx stops serving the vhost
	// while its certificate still exists.
	enabledAtReload := true
	prevExec := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemctl" && len(args) > 0 && args[0] == "reload" {
			_, err := os.Lstat(filepath.Join(enabl, dom+"-mta-sts.conf"))
			enabledAtReload = err == nil
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prevExec })

	params, _ := json.Marshal(domainDeleteParams{Domain: dom})
	if _, err := domainDeleteHandler(context.Background(), params); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{filepath.Join(enabl, dom+"-mta-sts.conf"), filepath.Join(avail, dom+"-mta-sts.conf"), filepath.Join(root, dom)} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind by domain.delete (err=%v)", p, err)
		}
	}
	if enabledAtReload {
		t.Error("nginx reloaded while the MTA-STS vhost was still enabled")
	}
	for _, p := range []string{filepath.Join(enabl, keep+"-mta-sts.conf"), filepath.Join(avail, keep+"-mta-sts.conf"), filepath.Join(root, keep)} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("another domain's %s was removed: %v", p, err)
		}
	}
}
