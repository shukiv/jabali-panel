package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// gh723ResumeDBRepo simulates a RESUME: the destination DB already exists, so
// ImportDatabases takes the AlreadyPresent path and skips the re-import.
type gh723ResumeDBRepo struct{ repository.DatabaseRepository }

func (gh723ResumeDBRepo) ExistsByUserAndName(context.Context, string, string) (bool, error) {
	return true, nil
}

// GH #723 resume gap: an earlier run imported the DB but died before the
// app-config rewrite. On resume the DB is AlreadyPresent → the dump loop skips
// it → without a credential the rewriter no-ops and the config keeps the dead
// source DB name. Under --preserve-source-state the resume must still seed the
// namespaced credential so the compat pass keeps the source creds and the
// config is namespaced.
func TestGH723_Resume_PreserveOn_RecoversCredential(t *testing.T) {
	extract := t.TempDir()
	if err := os.WriteFile(filepath.Join(extract, "notary_45635.mysql.sql"), []byte("-- dump\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed := &ParsedTarball{
		ExtractDir: extract,
		SourceUser: "notary",
		MySQLDumps: []string{filepath.Join(extract, "notary_45635.mysql.sql")},
		CompatUsers: []CompatUser{
			{Name: "notary_45635", Host: "localhost", Hash: gh723NativeHash,
				Grant: []CompatGrant{{SourceDB: "notary_45635", Privs: []string{"ALL"}}}},
		},
	}

	res, err := ImportDatabases(
		context.Background(),
		gh723ResumeDBRepo{}, gh723DBUserRepo{}, gh723GrantRepo{},
		&recordingAgent{},
		parsed,
		"01USERULID0000000000000000", "sewickley", true, // preserve ON
	)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}
	if res.AlreadyPresent != 1 {
		t.Fatalf("want AlreadyPresent=1 (resume path), got %d", res.AlreadyPresent)
	}

	cred, ok := res.Credentials["notary_45635"]
	if !ok {
		t.Fatalf("resume must seed the credential keyed by the source DB name (keys=%v)", keysOf(res.Credentials))
	}
	if cred.DBName != "sewickley_45635" {
		t.Errorf("cred.DBName = %q, want namespaced sewickley_45635", cred.DBName)
	}
	if !cred.preservesUser("notary_45635") {
		t.Errorf("compat pass must mark the recreated source user preserved on resume: %v", cred.PreservedUsers)
	}

	// The rewriter then keeps the source creds + namespaces DB_NAME.
	out, changed := rewriteWordPress(gh723PreserveWPConfig, res.Credentials)
	if !changed {
		t.Fatalf("wp-config not rewritten on resume:\n%s", out)
	}
	if !strings.Contains(out, "define('DB_NAME', 'sewickley_45635');") {
		t.Errorf("DB_NAME must be namespaced on resume:\n%s", out)
	}
	if !strings.Contains(out, "define( 'DB_PASSWORD', 'e4eb22f97bb143151c95' );") {
		t.Errorf("resume must keep the source password (preserve on):\n%s", out)
	}
}

// With preserve OFF, resume can't recover the panel-managed temp password, so it
// deliberately does NOT seed a credential (no empty/guessed password) — and says
// so in the summary.
func TestGH723_Resume_PreserveOff_DoesNotPopulate(t *testing.T) {
	extract := t.TempDir()
	if err := os.WriteFile(filepath.Join(extract, "notary_45635.mysql.sql"), []byte("-- dump\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed := &ParsedTarball{
		ExtractDir: extract,
		SourceUser: "notary",
		MySQLDumps: []string{filepath.Join(extract, "notary_45635.mysql.sql")},
	}

	res, err := ImportDatabases(
		context.Background(),
		gh723ResumeDBRepo{}, gh723DBUserRepo{}, gh723GrantRepo{},
		&recordingAgent{},
		parsed,
		"01USERULID0000000000000000", "sewickley", false, // preserve OFF
	)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}
	if _, ok := res.Credentials["notary_45635"]; ok {
		t.Errorf("preserve-off resume must not seed a credential (no recoverable password), got one")
	}
	// A clear warning must be present so the operator knows to reset the password.
	found := false
	for _, s := range res.Skipped {
		if strings.Contains(s, "password can't be recovered") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("preserve-off resume must warn that the password can't be recovered; skipped=%v", res.Skipped)
	}
}
