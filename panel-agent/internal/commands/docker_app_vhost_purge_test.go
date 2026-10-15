package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A deleted docker-app domain's certificate files go with its vhost: the
// self-signed directory and the Let's Encrypt lineage. Before, the delete
// removed only the vhost, so the lineage kept renewing a name the server no
// longer serves and the key stayed on disk. A disabled domain (the
// reconciler's call, no purge) keeps them.

// certFixture points the certificate roots at a temp dir and creates the
// self-signed directory and an (untracked) Let's Encrypt live + archive
// directory for each name.
func certFixture(t *testing.T, names ...string) (selfRoot, leRoot string) {
	t.Helper()
	tmp := t.TempDir()
	selfRoot, leRoot = filepath.Join(tmp, "self"), filepath.Join(tmp, "le")
	prevSelf, prevLE := baseSelfSignDir, sslLERoot
	baseSelfSignDir, sslLERoot = selfRoot, leRoot
	t.Cleanup(func() { baseSelfSignDir, sslLERoot = prevSelf, prevLE })
	for _, n := range names {
		for _, d := range []string{filepath.Join(selfRoot, n), filepath.Join(leRoot, "live", n), filepath.Join(leRoot, "archive", n)} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "fullchain.pem"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return selfRoot, leRoot
}

func certDirs(selfRoot, leRoot, name string) []string {
	return []string{filepath.Join(selfRoot, name), filepath.Join(leRoot, "live", name), filepath.Join(leRoot, "archive", name)}
}

func applyDockerVhost(t *testing.T, name string) {
	t.Helper()
	apply, _ := json.Marshal(dockerAppVhostApplyParams{
		DomainName: name, Upstream: "http://127.0.0.1:9000",
		SSLCertPath: "/c", SSLKeyPath: "/k",
	})
	if _, err := dockerAppVhostApplyHandler(context.Background(), apply); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

func removeDockerVhost(t *testing.T, name string, purge bool) error {
	t.Helper()
	rm, _ := json.Marshal(dockerAppVhostRemoveParams{DomainName: name, PurgeCertificates: purge})
	_, err := dockerAppVhostRemoveHandler(context.Background(), rm)
	return err
}

func TestDockerAppVhostRemove_PurgeRemovesCertificatesAfterReload(t *testing.T) {
	withDockerAppVhostTestDirs(t)
	const dom, keep = "gone.example.com", "keep.example.com"
	selfRoot, leRoot := certFixture(t, dom, keep)
	applyDockerVhost(t, dom)

	// The certificate must still exist when nginx reloads without the vhost:
	// removing it while a loaded vhost names it fails `nginx -t`.
	certAtReload := false
	nginxTestAndReload = func(context.Context) error {
		_, err := os.Stat(filepath.Join(selfRoot, dom))
		certAtReload = err == nil
		return nil
	}

	if err := removeDockerVhost(t, dom, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !certAtReload {
		t.Error("certificates were removed before nginx reloaded without the vhost")
	}
	for _, d := range certDirs(selfRoot, leRoot, dom) {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s left behind by a purging remove (err=%v)", d, err)
		}
	}
	for _, d := range certDirs(selfRoot, leRoot, keep) {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("another domain's %s was removed: %v", d, err)
		}
	}

	// Idempotent: the vhost is already gone, a second purge is harmless.
	if err := removeDockerVhost(t, dom, true); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestDockerAppVhostRemove_WithoutPurgeKeepsCertificates(t *testing.T) {
	av, _ := withDockerAppVhostTestDirs(t)
	const dom = "disabled.example.com"
	selfRoot, leRoot := certFixture(t, dom)
	applyDockerVhost(t, dom)

	if err := removeDockerVhost(t, dom, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(av, dom+"-dockerapp.conf")); !os.IsNotExist(err) {
		t.Errorf("vhost should be gone")
	}
	for _, d := range certDirs(selfRoot, leRoot, dom) {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("a disabled domain lost %s: %v", d, err)
		}
	}
}

func TestDockerAppVhostRemove_PurgeKeepsCertificatesAWebVhostUses(t *testing.T) {
	_, en := withDockerAppVhostTestDirs(t)
	const dom = "shared.example.com"
	selfRoot, leRoot := certFixture(t, dom)
	applyDockerVhost(t, dom)
	if err := os.WriteFile(filepath.Join(en, dom+".conf"), []byte("server {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := removeDockerVhost(t, dom, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, d := range certDirs(selfRoot, leRoot, dom) {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s removed while the web vhost %s.conf still uses it: %v", d, dom, err)
		}
	}
}

func TestDockerAppVhostRemove_FailedReloadKeepsCertificates(t *testing.T) {
	withDockerAppVhostTestDirs(t)
	const dom = "reloadfail.example.com"
	selfRoot, leRoot := certFixture(t, dom)
	applyDockerVhost(t, dom)
	nginxTestAndReload = func(context.Context) error { return errors.New("nginx: [emerg] test failed") }

	if err := removeDockerVhost(t, dom, true); err == nil {
		t.Fatal("remove should report the failed reload")
	}
	for _, d := range certDirs(selfRoot, leRoot, dom) {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s removed although nginx did not reload: %v", d, err)
		}
	}
}

// domain.delete reaps the docker-app vhost of the name before it reloads and
// before it removes the certificates. A user delete, or a delete on the
// Domains page, reaches a docker-app domain only through domain.delete. Left
// behind, the vhost names a removed certificate, so `nginx -t` fails for the
// whole server, and it keeps proxying to a loopback port the panel hands to
// the next app installed.
func TestDomainDelete_ReapsTheDockerAppVhost(t *testing.T) {
	av, en := withDockerAppVhostTestDirs(t)
	const dom, keep = "dockerapp-delete.test", "dockerapp-keep.test"
	selfRoot, leRoot := certFixture(t, dom)
	applyDockerVhost(t, dom)
	applyDockerVhost(t, keep)

	enabledAtReload := true
	prevExec := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemctl" && len(args) > 0 && args[0] == "reload" {
			_, err := os.Lstat(filepath.Join(en, dom+"-dockerapp.conf"))
			enabledAtReload = err == nil
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prevExec })

	params, _ := json.Marshal(domainDeleteParams{Domain: dom})
	if _, err := domainDeleteHandler(context.Background(), params); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{filepath.Join(en, dom+"-dockerapp.conf"), filepath.Join(av, dom+"-dockerapp.conf")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind by domain.delete (err=%v)", p, err)
		}
	}
	if enabledAtReload {
		t.Error("nginx reloaded while the docker-app vhost was still enabled")
	}
	for _, d := range certDirs(selfRoot, leRoot, dom) {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s left behind by domain.delete (err=%v)", d, err)
		}
	}
	for _, p := range []string{filepath.Join(en, keep+"-dockerapp.conf"), filepath.Join(av, keep+"-dockerapp.conf")} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("another domain's %s was removed: %v", p, err)
		}
	}
}

// A docker-app domain can share its certificate name with another vhost's:
// an app on mail.<domain> shares the lineage of that domain's mail vhost
// (`<domain>-mail.conf`). The purge keeps files any enabled vhost still names,
// or `nginx -t` fails for the whole server. A name that only starts the same
// way doesn't count.
func TestDockerAppVhostRemove_PurgeKeepsCertificatesAnotherVhostNames(t *testing.T) {
	_, en := withDockerAppVhostTestDirs(t)
	const dom, longer = "mail.live.example.com", "mail.live.example.com.au"
	selfRoot, leRoot := certFixture(t, dom, longer)
	applyDockerVhost(t, dom)
	applyDockerVhost(t, longer)
	mailVhost := "server {\n  ssl_certificate " + filepath.Join(leRoot, "live", dom, "fullchain.pem") + ";\n}\n"
	if err := os.WriteFile(filepath.Join(en, "live.example.com-mail.conf"), []byte(mailVhost), 0o644); err != nil {
		t.Fatal(err)
	}
	selfVhost := "server {\n  ssl_certificate_key " + filepath.Join(selfRoot, longer+"x", "privkey.pem") + ";\n}\n"
	if err := os.WriteFile(filepath.Join(en, "other.example.com.conf"), []byte(selfVhost), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := removeDockerVhost(t, dom, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, d := range certDirs(selfRoot, leRoot, dom) {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s removed while live.example.com-mail.conf still names the lineage: %v", d, err)
		}
	}

	if err := removeDockerVhost(t, longer, true); err != nil {
		t.Fatalf("remove %s: %v", longer, err)
	}
	for _, d := range certDirs(selfRoot, leRoot, longer) {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s kept because another vhost names a longer name (err=%v)", d, err)
		}
	}
}

// The self-signed directory counts as named too.
func TestDockerAppVhostRemove_PurgeKeepsASelfSignedDirAnotherVhostNames(t *testing.T) {
	_, en := withDockerAppVhostTestDirs(t)
	const dom = "app.example.org"
	selfRoot, leRoot := certFixture(t, dom)
	applyDockerVhost(t, dom)
	v := "server {\n  ssl_certificate " + filepath.Join(selfRoot, dom, "fullchain.pem") + ";\n}\n"
	if err := os.WriteFile(filepath.Join(en, "app.example.org-mail.conf"), []byte(v), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := removeDockerVhost(t, dom, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, d := range certDirs(selfRoot, leRoot, dom) {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s removed while another vhost names the self-signed directory: %v", d, err)
		}
	}
}
