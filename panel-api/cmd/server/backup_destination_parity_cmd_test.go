package main

import (
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-310: the CLI destination validator omitted the REST handler's
// shell-metacharacter rule, so a CLI-created SFTP destination could carry an
// injectable host/user that flows into restic's sftp.command. validateSFTPOpts
// now enforces the same shared boundary rule.
func TestValidateSFTPOpts_RejectsUnsafeHostUser(t *testing.T) {
	if err := validateSFTPOpts(&models.SFTPOptions{Host: "h;rm -rf /", User: "u", Path: "/backups"}); err == nil {
		t.Error("CLI must reject shell metacharacters in the sftp host")
	}
	if err := validateSFTPOpts(&models.SFTPOptions{Host: "h", User: "restic user", Path: "/backups"}); err == nil {
		t.Error("CLI must reject whitespace in the sftp user")
	}
	// A well-formed destination still validates.
	if err := validateSFTPOpts(&models.SFTPOptions{Host: "backup.example.com", User: "restic", Path: "/backups"}); err != nil {
		t.Errorf("a plain sftp destination must be accepted, got %v", err)
	}
}

// TestBuildReplacedSFTPBlock_ReplacesFromFlagsOnly is the JAB-310 AC5 core: a
// destination-update SFTP block is built VERBATIM from the flags and consults no
// stored value. A full block round-trips to exactly the supplied fields —
// nothing added, nothing defaulted — so an omitted field (port 0, empty
// key-path) stays empty rather than inheriting a stale stored value, which is
// the replace-by-contract that matches the REST update handler. The pre-JAB-310
// CLI overlaid onto the stored block; this is the discriminator against it.
func TestBuildReplacedSFTPBlock_ReplacesFromFlagsOnly(t *testing.T) {
	url, raw, err := buildReplacedSFTPBlock("backup.example.com", "restic", 0, "/backups", models.SFTPAuthKey, "")
	if err != nil {
		t.Fatalf("a well-formed block must validate: %v", err)
	}
	if url == "" {
		t.Fatal("composed restic URL must not be empty")
	}
	var got models.BackupDestinationExtraOptions
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("extra_options must unmarshal: %v", err)
	}
	if got.SFTP == nil {
		t.Fatal("extra_options must carry an SFTP block")
	}
	want := models.SFTPOptions{Host: "backup.example.com", User: "restic", Port: 0, Path: "/backups", Auth: models.SFTPAuthKey, KeyPath: ""}
	if *got.SFTP != want {
		t.Fatalf("block not built verbatim from flags: got %+v want %+v", *got.SFTP, want)
	}
}

// TestBuildReplacedSFTPBlock_RejectsPartialBlock: because the block is built only
// from the flags (no overlay from the stored row), a partial edit must be
// REJECTED by the full-block validator. Under the pre-JAB-310 overlay a lone
// --sftp-host silently kept the stored user/path; now it is an error.
func TestBuildReplacedSFTPBlock_RejectsPartialBlock(t *testing.T) {
	if _, _, err := buildReplacedSFTPBlock("newhost.example.com", "", 0, "", "", ""); err == nil {
		t.Fatal("a block missing user+path must be rejected (full-block replace, not overlay)")
	}
}

// TestSFTPPasswordWriteAllowed_GatesAuthAndKind pins the JAB-310 AC5 password
// gate: --sftp-password is an independent credential write, but it must only
// land on an sftp destination whose effective auth is "password". Writing an
// SSHPASS to a key-auth (or non-sftp) destination is meaningless and must be
// rejected, not silently stored.
func TestSFTPPasswordWriteAllowed_GatesAuthAndKind(t *testing.T) {
	if err := sftpPasswordWriteAllowed(models.BackupDestinationKindSFTP, models.SFTPAuthPassword); err != nil {
		t.Errorf("password write must be allowed for a password-auth sftp dest: %v", err)
	}
	if err := sftpPasswordWriteAllowed(models.BackupDestinationKindSFTP, models.SFTPAuthKey); err == nil {
		t.Error("password write must be rejected for a key-auth destination")
	}
	if err := sftpPasswordWriteAllowed(models.BackupDestinationKindSFTP, ""); err == nil {
		t.Error("password write must be rejected when auth is unset (defaults to key)")
	}
	if err := sftpPasswordWriteAllowed(models.BackupDestinationKindLocal, models.SFTPAuthPassword); err == nil {
		t.Error("password write must be rejected for a non-sftp destination kind")
	}
}
