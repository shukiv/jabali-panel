package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// TestQuotaEnabledOn covers the GH #1053 isolated-create preflight: quota is
// "on" only when `quotaon -pu` reports it. The command EXIT CODE is ignored on
// purpose — `quotaon -p` returns non-zero even when quota is fully enabled on
// the fleet OS (Debian 13, observed live: "... is on (enforced)", exit 1), so
// gating on the exit code was a false negative that dead-ended isolated FTP.
// Only the printed "is on" text is authoritative; a missing binary / unsupported
// fs (empty output) or an "is off" line still fail closed.
func TestQuotaEnabledOn(t *testing.T) {
	orig := execCommandContext
	t.Cleanup(func() { execCommandContext = orig })

	cases := []struct {
		name string
		out  string
		rc   int
		want bool
	}{
		{"on, exit 0", "user quota on / (/dev/root) is on", 0, true},
		// The regression: quota IS on but quotaon -p exits non-zero (Debian 13).
		{"on but exit 1 (debian13 quotaon -p)", "user quota on / (/dev/sda1) is on (enforced)", 1, true},
		{"off, exit 1", "user quota on / is off", 1, false},
		{"binary error, empty output", "", 127, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, rc := tc.out, tc.rc
			execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				// Emit the canned output AND the exit code together, so the real
				// CombinedOutput + exit-status path is exercised through the seam.
				return exec.CommandContext(ctx, "sh", "-c", "printf '%s\\n' \""+out+"\"; exit "+strconv.Itoa(rc))
			}
			if got := quotaEnabledOn(context.Background(), "/"); got != tc.want {
				t.Fatalf("quotaEnabledOn=%v want %v", got, tc.want)
			}
		})
	}
}

func testTenant() *ftpTenant {
	return &ftpTenant{Username: "bob", UID: 1001, GID: 1001, HomeDir: "/home/bob"}
}

func TestFtpJailPathFor(t *testing.T) {
	t.Setenv("JABALI_FTP_JAIL_ROOT", "/var/lib/jabali-ftp-jails")
	got := ftpJailPathFor(testTenant(), "bob_printer")
	want := "/var/lib/jabali-ftp-jails/bob/bob_printer"
	if got != want {
		t.Fatalf("ftpJailPathFor = %q, want %q", got, want)
	}
}

func TestValidateIsolatedCreate(t *testing.T) {
	t.Setenv("JABALI_FTP_JAIL_ROOT", "/var/lib/jabali-ftp-jails")
	tenant := testTenant()
	goodJail := ftpJailPathFor(tenant, "bob_printer")

	base := func() ftpAccountCreateParams {
		return ftpAccountCreateParams{
			TenantUsername: "bob",
			Username:       "bob_printer",
			HomePath:       "/home/bob/ftp/printer",
			Password:       "x",
			Isolated:       true,
			UID:            1000000001,
			QuotaMB:        100,
			QuotaMount:     "/home",
			JailPath:       goodJail,
		}
	}

	tests := []struct {
		name    string
		mutate  func(p *ftpAccountCreateParams)
		wantErr bool
	}{
		{"happy", func(p *ftpAccountCreateParams) {}, false},
		{"uid below floor", func(p *ftpAccountCreateParams) { p.UID = 1002 }, true},
		{"uid equals tenant", func(p *ftpAccountCreateParams) { p.UID = uint32(tenant.UID) }, true},
		{"zero quota", func(p *ftpAccountCreateParams) { p.QuotaMB = 0 }, true},
		{"no quota mount", func(p *ftpAccountCreateParams) { p.QuotaMount = "" }, true},
		{"jail mismatch", func(p *ftpAccountCreateParams) { p.JailPath = "/tmp/evil" }, true},
		{"jail not canonical (wrong tenant)", func(p *ftpAccountCreateParams) {
			p.JailPath = "/var/lib/jabali-ftp-jails/other/bob_printer"
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base()
			tc.mutate(&p)
			aerr := validateIsolatedCreate(p, tenant)
			if (aerr != nil) != tc.wantErr {
				t.Fatalf("validateIsolatedCreate err=%v, wantErr=%v", aerr, tc.wantErr)
			}
		})
	}
}

// TestValidateFtpSSHDAccountJailChroot proves the sshd validator accepts a
// root-owned jail chroot (GH #1145) as well as the legacy /home chroot, and
// still rejects an arbitrary path.
func TestValidateFtpSSHDAccountJailChroot(t *testing.T) {
	t.Setenv("JABALI_FTP_JAIL_ROOT", "/var/lib/jabali-ftp-jails")
	tests := []struct {
		name      string
		chrootDir string
		startDir  string
		wantErr   bool
	}{
		{"legacy home chroot", "/home/bob", "/ftp/printer", false},
		{"jail chroot", "/var/lib/jabali-ftp-jails/bob/bob_printer", "/data", false},
		{"arbitrary path rejected", "/etc", "/", true},
		{"jail with dotdot rejected", "/var/lib/jabali-ftp-jails/bob/../etc", "/", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			aerr := validateFtpSSHDAccount(ftpSSHDSyncAccount{
				Username:  "bob_printer",
				ChrootDir: tc.chrootDir,
				StartDir:  tc.startDir,
			})
			if (aerr != nil) != tc.wantErr {
				t.Fatalf("validateFtpSSHDAccount(%q) err=%v, wantErr=%v", tc.chrootDir, aerr, tc.wantErr)
			}
		})
	}
}

// TestTeardownIsolatedJailPreservesSourceOnSameFs is the regression guard for
// the same-device data-loss bug: on the fleet /home is not a separate mount, so
// a bind mount does NOT change st_dev at the mountpoint. A stat-based "is it
// mounted?" probe returns false, so a naive teardown would skip the unmount and
// RemoveAll straight through the LIVE bind mount into the tenant's real files.
// teardownIsolatedJail must unmount blind (until EINVAL) BEFORE removing, so the
// jail goes and the source subtree survives. Root-gated (bind mount needs
// CAP_SYS_ADMIN) + GH #994 host-mutation gate.
func TestTeardownIsolatedJailPreservesSourceOnSameFs(t *testing.T) {
	requireHostMutationAllowed(t)
	if os.Geteuid() != 0 {
		t.Skip("bind mount needs root")
	}
	root := t.TempDir() // source + jail both under TMPDIR = same filesystem
	t.Setenv("JABALI_FTP_JAIL_ROOT", filepath.Join(root, "jails"))

	src := filepath.Join(root, "home", "sub")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(src, "keep.txt")
	if err := os.WriteFile(marker, []byte("tenant-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	jail := filepath.Join(ftpJailRoot(), "bob", "bob_printer")
	mp := filepath.Join(jail, ftpJailMountpoint)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mount(src, mp, "", syscall.MS_BIND, ""); err != nil {
		t.Fatalf("bind mount: %v", err)
	}
	// Guarantee cleanup even if an assertion below fails before teardown runs.
	defer func() { _ = syscall.Unmount(mp, syscall.MNT_DETACH) }()

	if _, err := os.Stat(filepath.Join(mp, "keep.txt")); err != nil {
		t.Fatalf("marker not visible through the mount (setup broken): %v", err)
	}

	if aerr := teardownIsolatedJail(context.Background(), jail); aerr != nil {
		t.Fatalf("teardown returned error: %v", aerr)
	}
	if _, err := os.Stat(jail); !os.IsNotExist(err) {
		t.Fatalf("jail not removed after teardown: stat err=%v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("SOURCE DATA DELETED by teardown — marker gone: %v", err)
	}
}
