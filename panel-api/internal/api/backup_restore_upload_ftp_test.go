package api

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: the agent restores an uploaded backup's FTP subaccount passwords
// only into an account the restore created, whose home holds nothing but the
// file's data. Each door says whether it created the account, and only the
// first agent pass (not the mail pass) asks.

// ftpUploadReply is a first pass that restored a mail stage and staged
// alice_web's password, then a mail pass.
func ftpUploadReply(n int) string {
	if n == 0 {
		return `{"upload_confinement_enforced":true,"stages":[{"name":"home"},{"name":"mail"}],"ftp_passwords_staged":["alice_web"]}`
	}
	return `{"upload_confinement_enforced":true}`
}

func checkFtpPasswordsAsked(t *testing.T, calls []map[string]any, rebuilds []uploadedData, created bool) {
	t.Helper()
	if len(calls) != 2 {
		t.Fatalf("agent passes = %d (%v), want the restore and the mail pass", len(calls), calls)
	}
	if got, _ := calls[0]["ftp_passwords"].(bool); got != created {
		t.Errorf("first pass ftp_passwords = %v, want %v", calls[0]["ftp_passwords"], created)
	}
	if _, ok := calls[1]["ftp_passwords"]; ok {
		t.Errorf("the mail pass asked for FTP passwords: %v", calls[1])
	}
	if len(rebuilds) != 1 || strings.Join(rebuilds[0].ftpPasswordsStaged, ",") != "alice_web" {
		t.Errorf("metadata rebuild got %+v, want the staged alice_web", rebuilds)
	}
	// The rebuild restores the file's SSH keys only into a created account.
	if len(rebuilds) == 1 && rebuilds[0].accountCreated != created {
		t.Errorf("metadata rebuild accountCreated = %v, want %v", rebuilds[0].accountCreated, created)
	}
}

func TestRunUploadRestore_AsksForFtpPasswordsOnlyForACreatedAccount(t *testing.T) {
	for _, created := range []bool{true, false} {
		rebuilds := captureRebuilds(t)
		h, a, calls := ucUploadPasses(t, ftpUploadReply)
		a.userCreated = created
		h.runUploadRestore(a)
		checkFtpPasswordsAsked(t, *calls, rebuilds(), created)
	}
}

func TestRunUploadedBackupRestore_AsksForFtpPasswordsOnlyForACreatedAccount(t *testing.T) {
	for _, created := range []bool{true, false} {
		rebuilds := captureRebuilds(t)
		h, a, calls := ucUploadPasses(t, ftpUploadReply)
		h.cfg.UploadedBackups = newMemUploaded()
		h.runUploadedBackupRestore(&models.UploadedBackup{ID: "B1"}, a.path, "alice", "T", nil, created, true, restoreSkips{})
		checkFtpPasswordsAsked(t, *calls, rebuilds(), created)
	}
}

func TestRestoreMetadataDeps_FtpPasswordsStaged(t *testing.T) {
	h := &backupHandler{}
	d := h.restoreMetadataDeps(&uploadedData{ftpPasswordsStaged: []string{"alice_web"}})
	if !d.FtpPasswordsStaged["alice_web"] || d.FtpPasswordsStaged["alice_ro"] {
		t.Fatalf("FtpPasswordsStaged = %v, want alice_web only", d.FtpPasswordsStaged)
	}
}

func TestRestoreMetadataDeps_AccountCreated(t *testing.T) {
	h := &backupHandler{}
	for _, created := range []bool{true, false} {
		if d := h.restoreMetadataDeps(&uploadedData{accountCreated: created}); d.AccountCreated != created {
			t.Errorf("AccountCreated = %v, want %v", d.AccountCreated, created)
		}
	}
}
