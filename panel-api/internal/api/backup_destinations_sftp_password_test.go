package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// sftpPwAgent is a creds-write fake that captures the env payload of the last
// backup.dest.creds_write, so a test can assert the stored-auth path writes THIS
// SSHPASS value (not merely that a write fired) and leaks no other key.
type sftpPwAgent struct {
	writes  int
	lastEnv map[string]string
}

func (a *sftpPwAgent) Call(_ context.Context, cmd string, args any) (json.RawMessage, error) {
	if cmd == "backup.dest.creds_write" {
		a.writes++
		var p struct {
			Env map[string]string `json:"env"`
		}
		raw, _ := json.Marshal(args)
		_ = json.Unmarshal(raw, &p)
		a.lastEnv = p.Env
		return json.RawMessage(`{"path":"/etc/jabali-panel/restic-remotes/dst-1.env"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

// sftpDestWithAuth returns a stored SFTP destination whose extra_options carry
// the given auth ("key"|"password"), the shape the update handler reads to
// derive effective auth when the request sends no fresh sftp block.
func sftpDestWithAuth(id, auth string) *models.BackupDestination {
	raw, _ := json.Marshal(models.BackupDestinationExtraOptions{
		SFTP: &models.SFTPOptions{Host: "backup.example.com", User: "restic", Path: "/backups", Auth: auth},
	})
	return &models.BackupDestination{
		ID: id, Name: "n", Kind: models.BackupDestinationKindSFTP, ExtraOptions: raw,
	}
}

// A password-only rotation — sftp_password with NO sftp block — must land on a
// destination whose STORED auth is already "password", writing exactly that
// SSHPASS. This is the JAB-310 gap: the old handler coupled the SSHPASS write to
// a present req.SFTP block, so this request was silently dropped with a 200.
func TestSFTPPassword_PasswordOnlyRotation_HonoursStoredAuth(t *testing.T) {
	ag := &sftpPwAgent{}
	repo := &updFakeDestRepo{getDest: sftpDestWithAuth("dst-1", models.SFTPAuthPassword)}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{"sftp_password": "newpass"})

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if ag.writes != 1 {
		t.Fatalf("creds_write fired %d times, want 1", ag.writes)
	}
	if got := ag.lastEnv["SSHPASS"]; got != "newpass" {
		t.Fatalf("SSHPASS = %q, want %q", got, "newpass")
	}
	if len(ag.lastEnv) != 1 {
		t.Fatalf("creds env leaked extra keys: %v", ag.lastEnv)
	}
}

// sftp_password against a stored KEY-auth destination is meaningless — it must
// fail loud (400), never be silently stored.
func TestSFTPPassword_KeyAuthDest_Rejected(t *testing.T) {
	ag := &sftpPwAgent{}
	repo := &updFakeDestRepo{getDest: sftpDestWithAuth("dst-1", models.SFTPAuthKey)}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{"sftp_password": "x"})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "sftp_password_not_applicable") {
		t.Fatalf("body = %s, want sftp_password_not_applicable", w.Body.String())
	}
	if ag.writes != 0 {
		t.Fatalf("creds_write fired %d times, want 0", ag.writes)
	}
}

// sftp_password against a non-sftp (local) destination must fail loud (400).
func TestSFTPPassword_LocalDest_Rejected(t *testing.T) {
	ag := &sftpPwAgent{}
	repo := &updFakeDestRepo{getDest: &models.BackupDestination{
		ID: "dst-1", Name: "n", Kind: models.BackupDestinationKindLocal,
	}}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{"sftp_password": "x"})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if ag.writes != 0 {
		t.Fatalf("creds_write fired %d times, want 0", ag.writes)
	}
}

// The sibling silent drop: a FULL key-auth block plus sftp_password. The old
// handler wrote SSHPASS only when req.SFTP.Auth == password, so a key-auth block
// carrying a password silently dropped it (200). The effective-auth gate now
// rejects it (400) — the same defect class in the same lines, fixed together.
func TestSFTPPassword_KeyAuthBlockWithPassword_Rejected(t *testing.T) {
	ag := &sftpPwAgent{}
	repo := &updFakeDestRepo{getDest: sftpDestWithAuth("dst-1", models.SFTPAuthPassword)}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{
		"sftp": map[string]any{
			"host": "backup.example.com", "user": "restic", "path": "/backups", "auth": "key",
		},
		"sftp_password": "x",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "sftp_password_not_applicable") {
		t.Fatalf("body = %s, want sftp_password_not_applicable", w.Body.String())
	}
	if ag.writes != 0 {
		t.Fatalf("creds_write fired %d times, want 0", ag.writes)
	}
}

// An empty sftp_password means "absent" (omitempty) — a password-unchanged edit.
// It must write nothing and leave any stored SSHPASS untouched, not 400.
func TestSFTPPassword_Empty_NoOp(t *testing.T) {
	ag := &sftpPwAgent{}
	repo := &updFakeDestRepo{getDest: sftpDestWithAuth("dst-1", models.SFTPAuthPassword)}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{"sftp_password": ""})

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if ag.writes != 0 {
		t.Fatalf("creds_write fired %d times on empty password, want 0", ag.writes)
	}
}

// Regression pin: the pre-existing working path — a full password-auth block
// plus sftp_password in one request — must still write the SSHPASS.
func TestSFTPPassword_FullPasswordBlock_StillWrites(t *testing.T) {
	ag := &sftpPwAgent{}
	repo := &updFakeDestRepo{getDest: sftpDestWithAuth("dst-1", models.SFTPAuthKey)}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{
		"sftp": map[string]any{
			"host": "backup.example.com", "user": "restic", "path": "/backups", "auth": "password",
		},
		"sftp_password": "secret",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if ag.writes != 1 {
		t.Fatalf("creds_write fired %d times, want 1", ag.writes)
	}
	if got := ag.lastEnv["SSHPASS"]; got != "secret" {
		t.Fatalf("SSHPASS = %q, want %q", got, "secret")
	}
}

// Corrupt stored extra_options plus a password-only request must fail closed
// (400), never write: ExtraOptionsTyped yields an empty struct on bad JSON, so
// effective auth is "" and the gate rejects — the client can always resend a
// full block.
func TestSFTPPassword_CorruptExtraOptions_FailClosed(t *testing.T) {
	ag := &sftpPwAgent{}
	repo := &updFakeDestRepo{getDest: &models.BackupDestination{
		ID: "dst-1", Name: "n", Kind: models.BackupDestinationKindSFTP,
		ExtraOptions: json.RawMessage(`{not json`),
	}}
	h := &backupDestinationHandler{repo: repo, agent: ag}

	w := updDo(h, "dst-1", map[string]any{"sftp_password": "x"})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if ag.writes != 0 {
		t.Fatalf("creds_write fired %d times, want 0", ag.writes)
	}
}
