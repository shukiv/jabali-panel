package main

import (
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
