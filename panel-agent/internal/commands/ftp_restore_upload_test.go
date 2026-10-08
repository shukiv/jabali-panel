package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// stubFtpUsers makes ftpUserExists true only for the names given.
func stubFtpUsers(t *testing.T, existing ...string) {
	t.Helper()
	prev := ftpUserExists
	ftpUserExists = func(name string) bool {
		for _, e := range existing {
			if name == e {
				return true
			}
		}
		return false
	}
	t.Cleanup(func() { ftpUserExists = prev })
}

func credStaged(username string) bool {
	_, err := os.Stat(filepath.Join(ftpRestoreCredDir, username))
	return err == nil
}

// GH #1993: an uploaded backup's FTP passwords are staged only for the
// target account's own subaccounts that aren't on this server yet.
func TestStageUploadedFtpPasswords_OnlyTheAccountsNewSubaccounts(t *testing.T) {
	ftpRestoreCredDir = t.TempDir()
	stubFtpUsers(t, "bob_old")
	now := time.Unix(1_700_000_000, 0)
	meta := &backup.AccountMetadata{FtpAccounts: []backup.MetadataFtpAccount{
		{Username: "bob_web", UID: u32(50010), Isolated: true, PasswordShadow: "$6$a$b"},
		{Username: "bob_legacy", UID: u32(50011), PasswordShadow: "$6$c$d"},
		{Username: "alice_web", PasswordShadow: "$6$e$f"},
		{Username: "bob_old", PasswordShadow: "$6$g$h"},
		{Username: "bob_nopw"},
		{Username: "bob_Bad", PasswordShadow: "$6$i$j"},
	}}
	staged, skipped := stageUploadedFtpPasswords(meta, "bob", now)
	if want := []string{"bob_web", "bob_legacy"}; !reflect.DeepEqual(staged, want) {
		t.Fatalf("staged = %v, want %v", staged, want)
	}
	if len(skipped) != 3 {
		t.Fatalf("skipped = %v, want alice_web, bob_old and bob_Bad", skipped)
	}
	for _, name := range []string{"alice_web", "bob_old", "bob_nopw", "bob_Bad"} {
		if credStaged(name) {
			t.Errorf("%s: staged, want not", name)
		}
	}
	if h, ok := consumeFtpRestoreCred("bob_web", "bob", u32(50010), now); !ok || h != "$6$a$b" {
		t.Errorf("bob_web: got %q %v, want its hash", h, ok)
	}
	// A legacy alias takes its hash without a uid, as its create asks.
	if h, ok := consumeFtpRestoreCred("bob_legacy", "bob", nil, now); !ok || h != "$6$c$d" {
		t.Errorf("bob_legacy: got %q %v, want its hash", h, ok)
	}
}

// A tenant name can contain '_': bob_x_web passes as bob's subaccount, so
// only tenant bob's create may take the hash, never tenant bob_x's.
func TestStageUploadedFtpPasswords_OnlyThatTenantTakesTheHash(t *testing.T) {
	ftpRestoreCredDir = t.TempDir()
	stubFtpUsers(t)
	now := time.Unix(1_700_000_000, 0)
	meta := &backup.AccountMetadata{FtpAccounts: []backup.MetadataFtpAccount{
		{Username: "bob_x_web", PasswordShadow: "$6$a$b"},
	}}
	if staged, _ := stageUploadedFtpPasswords(meta, "bob", now); len(staged) != 1 {
		t.Fatalf("staged = %v, want bob_x_web", staged)
	}
	if h, ok := consumeFtpRestoreCred("bob_x_web", "bob_x", nil, now); ok || h != "" {
		t.Fatalf("tenant bob_x took bob's staged hash")
	}
	if credStaged("bob_x_web") {
		t.Error("a refused staged hash must be deleted")
	}
	stageUploadedFtpPasswords(meta, "bob", now)
	if _, ok := consumeFtpRestoreCred("bob_x_web", "bob", nil, now); !ok {
		t.Error("tenant bob must take its own staged hash")
	}
}

// A hash a backup job staged names no tenant, and is taken as before.
func TestConsumeFtpRestoreCred_UnboundHashAnyTenant(t *testing.T) {
	ftpRestoreCredDir = t.TempDir()
	now := time.Unix(1_700_000_000, 0)
	_ = writeFtpRestoreCred("t1_web", nil, "$6$a$b", now)
	if _, ok := consumeFtpRestoreCred("t1_web", "t1", nil, now); !ok {
		t.Error("a job restore's staged hash must still be taken")
	}
}

// The create takes a staged hash only for its own tenant.
func TestSetFtpAccountPassword_TakesOnlyItsTenantsHash(t *testing.T) {
	ftpRestoreCredDir = t.TempDir()
	now := time.Now()
	p := ftpAccountCreateParams{Username: "bob_x_web", Password: "throwaway", PreferRestoreCredential: true}

	_ = writeFtpRestoreCredFor("bob_x_web", "bob", nil, "$6$a$b", now)
	cmds := uploadExecRecorder(t)
	if aerr := setFtpAccountPassword(context.Background(), p, "bob_x"); aerr != nil {
		t.Fatal(aerr)
	}
	if want := []string{"chpasswd "}; !reflect.DeepEqual(*cmds, want) {
		t.Fatalf("tenant bob_x: ran %q, want the throwaway (%q)", *cmds, want)
	}

	_ = writeFtpRestoreCredFor("bob_x_web", "bob", nil, "$6$a$b", now)
	*cmds = nil
	if aerr := setFtpAccountPassword(context.Background(), p, "bob"); aerr != nil {
		t.Fatal(aerr)
	}
	if want := []string{"chpasswd -e"}; !reflect.DeepEqual(*cmds, want) {
		t.Fatalf("tenant bob: ran %q, want the staged hash (%q)", *cmds, want)
	}
}

func uploadReplyWithFtp(t *testing.T) *backupRestoreFromTarResult {
	t.Helper()
	meta, err := json.Marshal(backup.AccountMetadata{FtpAccounts: []backup.MetadataFtpAccount{
		{Username: "bob_web", PasswordShadow: "$6$a$b"},
		{Username: "alice_web", PasswordShadow: "$6$c$d"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &backupRestoreFromTarResult{Metadata: meta}
}

// The upload restore stages the file's FTP passwords only when the panel
// asks (the restore created the account), and says what it staged.
func TestUploadRestore_StagesFtpPasswordsWhenAsked(t *testing.T) {
	stubFtpUsers(t)
	now := time.Unix(1_700_000_000, 0)
	upload := restoreEnforcement{Mode: restoreModeUpload}

	ftpRestoreCredDir = t.TempDir()
	r := uploadReplyWithFtp(t)
	r.stageFtpPasswords(upload, "bob", true, now)
	if !reflect.DeepEqual(r.FTPPasswordsStaged, []string{"bob_web"}) || !credStaged("bob_web") {
		t.Fatalf("asked: staged %v, want bob_web", r.FTPPasswordsStaged)
	}
	if !hasWarning(r.Applied, "ftp: staged 1 subaccount password") {
		t.Errorf("applied = %v, want the staged line", r.Applied)
	}
	if !hasWarning(r.Warnings, `"alice_web": not an FTP account of bob`) {
		t.Errorf("warnings = %v, want alice_web's", r.Warnings)
	}

	ftpRestoreCredDir = t.TempDir()
	r = uploadReplyWithFtp(t)
	r.stageFtpPasswords(upload, "bob", false, now)
	if r.FTPPasswordsStaged == nil || len(r.FTPPasswordsStaged) != 0 || credStaged("bob_web") {
		t.Fatalf("not asked: staged %v, want an empty list and no file", r.FTPPasswordsStaged)
	}

	ftpRestoreCredDir = t.TempDir()
	r = uploadReplyWithFtp(t)
	r.stageFtpPasswords(restoreEnforcement{Mode: "tenant"}, "bob", true, now)
	if r.FTPPasswordsStaged != nil || credStaged("bob_web") {
		t.Fatalf("tenant mode: staged %v, want nothing", r.FTPPasswordsStaged)
	}
}

// The wire names the panel sends and reads (backup_restore_upload_confine.go).
func TestBackupRestoreFromTar_FtpPasswordsWire(t *testing.T) {
	var p backupRestoreFromTarParams
	if err := json.Unmarshal([]byte(`{"ftp_passwords":true}`), &p); err != nil || !p.FTPPasswords {
		t.Fatalf("ftp_passwords not read: %+v %v", p, err)
	}
	b, err := json.Marshal(backupRestoreFromTarResult{FTPPasswordsStaged: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if v, ok := m["ftp_passwords_staged"]; !ok || v == nil {
		t.Fatalf("reply = %s, want ftp_passwords_staged as a list", b)
	}
}
