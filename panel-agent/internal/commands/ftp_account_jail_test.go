package commands

import (
	"os"
	"path/filepath"
	"testing"
)

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
			UID:            500001,
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

func TestIsMountpointFalseOnPlainDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "child")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if isMountpoint(sub) {
		t.Fatalf("plain dir %q reported as mountpoint", sub)
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
