package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #723 — migrated WordPress (and Joomla/Drupal/Magento) sites end up with a
// broken DB config after migration. Reproduced on johnnyq's REAL HestiaCP
// backup (notary.2026-07-27_12-55-08.tar): the account has two databases,
// notary_45635 (WordPress, sewickleynotary.com) and notary_itflow (itFlow).
//
// johnnyq's actual wp-config.php DB block (verbatim from the backup):
//
//	define( 'DB_NAME', 'notary_45635' );
//	define( 'DB_USER', 'notary_45635' );
//	define( 'DB_PASSWORD', 'e4eb22f97bb143151c95' );
//	define( 'DB_HOST', 'localhost' );
//
// Root cause: ImportDatabases keyed res.Credentials by the NAMESPACED
// destination name (finalName = <target>_<logical>), but the app-config
// rewriters look the credential up by the SOURCE DB name they read out of the
// config file. Keying by finalName only ever matched when the target account
// shared the source's name; a multi-DB account migrated into a differently
// named account missed, the len(creds)==1 fallback couldn't save it, and
// wp-config was left pointing at a DB that no longer exists.
//
// Fix: key res.Credentials by the source DB name (`base`); the value still
// carries the namespaced DBName/DBUser + panel-managed password, so the rewrite
// produces the panel-namespaced config (the intended behavior — namespacing is
// always applied and the php files are rewritten to the namespaced creds).

const gh723JohnnyqWPConfig = `<?php
define( 'DB_NAME', 'notary_45635' );
define( 'DB_USER', 'notary_45635' );
define( 'DB_PASSWORD', 'e4eb22f97bb143151c95' );
define( 'DB_HOST', 'localhost' );
$table_prefix = 'wp_JV8O5_';
require_once ABSPATH . 'wp-settings.php';
`

// TestGH723_RewriteWordPress_MultiDB_KeyedBySourceName is the rewriter-side
// contract: given a credential map keyed by the SOURCE DB name (as the fixed
// ImportDatabases now produces), johnnyq's real multi-DB wp-config is rewritten
// to the namespaced destination credentials.
func TestGH723_RewriteWordPress_MultiDB_KeyedBySourceName(t *testing.T) {
	// Fixed keying: source DB name -> namespaced credential. Two DBs, target
	// account "johnnyq" (differs from source "notary").
	creds := map[string]DBCredential{
		"notary_45635":  {DBName: "johnnyq_45635", DBUser: "johnnyq_45635", Password: "PANELPW_45635"},
		"notary_itflow": {DBName: "johnnyq_itflow", DBUser: "johnnyq_itflow", Password: "PANELPW_itflow"},
	}

	out, changed := rewriteWordPress(gh723JohnnyqWPConfig, creds)
	if !changed {
		t.Fatalf("wp-config was NOT rewritten (GH #723 regression) — multi-DB lookup missed")
	}
	// Namespaced destination DB/user spliced in.
	// RewriteWPConfigDB normalizes the define() to `define('KEY', 'val');`
	// (no inner spaces) — that is the shared, proven rewriter's output.
	for _, want := range []string{
		"define('DB_NAME', 'johnnyq_45635');",
		"define('DB_USER', 'johnnyq_45635');",
		"define('DB_PASSWORD', 'PANELPW_45635');",
		"define('DB_HOST', 'localhost');",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rewritten wp-config missing %q\n---\n%s", want, out)
		}
	}
	// The dead source DB name / user must be gone.
	if strings.Contains(out, "'notary_45635'") {
		t.Errorf("rewritten wp-config still references the source DB notary_45635:\n%s", out)
	}
	if strings.Contains(out, "e4eb22f97bb143151c95") {
		t.Errorf("rewritten wp-config still carries the source DB password")
	}
}

// TestGH723_RewriteWordPress_FinalNameKeyingMisses documents WHY the map must
// be keyed by the source name: with the OLD (namespaced) keying and >1 DB, the
// rewrite silently no-ops and wp-config keeps the dead source DB name.
func TestGH723_RewriteWordPress_FinalNameKeyingMisses(t *testing.T) {
	oldKeying := map[string]DBCredential{
		"johnnyq_45635":  {DBName: "johnnyq_45635", DBUser: "johnnyq_45635", Password: "PANELPW_45635"},
		"johnnyq_itflow": {DBName: "johnnyq_itflow", DBUser: "johnnyq_itflow", Password: "PANELPW_itflow"},
	}
	_, changed := rewriteWordPress(gh723JohnnyqWPConfig, oldKeying)
	if changed {
		t.Fatalf("expected the pre-fix miss (multi-DB, finalName keys) to no-op; got changed=true")
	}
}

// --- fakes for the ImportDatabases seam test -------------------------------

type gh723DBRepo struct{ repository.DatabaseRepository }

func (gh723DBRepo) ExistsByUserAndName(context.Context, string, string) (bool, error) {
	return false, nil
}
func (gh723DBRepo) Create(context.Context, *models.Database) error { return nil }

type gh723DBUserRepo struct {
	repository.DatabaseUserRepository
}

func (gh723DBUserRepo) Create(context.Context, *models.DatabaseUser) error { return nil }

type gh723GrantRepo struct {
	repository.DatabaseUserGrantRepository
}

func (gh723GrantRepo) Create(context.Context, *models.DatabaseUserGrant) error { return nil }

// TestGH723_ImportDatabases_CredentialsKeyedBySourceName guards the fix at the
// seam that actually had the bug: ImportDatabases must key res.Credentials by
// the SOURCE DB name (what the config files reference), not the namespaced
// destination name. Multi-DB account, target ("johnnyq") differs from source
// ("notary") — the exact shape of johnnyq's notary backup.
func TestGH723_ImportDatabases_CredentialsKeyedBySourceName(t *testing.T) {
	extract := t.TempDir()
	// Two dumps named like Hestia's: <source-db>.mysql.sql.
	for _, db := range []string{"notary_45635", "notary_itflow"} {
		if err := os.WriteFile(filepath.Join(extract, db+".mysql.sql"), []byte("-- dump\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	parsed := &ParsedTarball{
		ExtractDir: extract,
		SourceUser: "notary",
		MySQLDumps: []string{
			filepath.Join(extract, "notary_45635.mysql.sql"),
			filepath.Join(extract, "notary_itflow.mysql.sql"),
		},
	}

	res, err := ImportDatabases(
		context.Background(),
		gh723DBRepo{}, gh723DBUserRepo{}, gh723GrantRepo{},
		&recordingAgent{},
		parsed,
		"01USERULID0000000000000000", "johnnyq", false,
	)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}

	// Keys must be the SOURCE DB names the config files reference.
	for _, srcName := range []string{"notary_45635", "notary_itflow"} {
		cred, ok := res.Credentials[srcName]
		if !ok {
			t.Fatalf("res.Credentials missing source-name key %q (keys=%v) — GH #723 regression", srcName, keysOf(res.Credentials))
		}
		// Value still carries the namespaced destination name/user.
		wantDest := "johnnyq_" + strings.TrimPrefix(srcName, "notary_")
		if cred.DBName != wantDest || cred.DBUser != wantDest {
			t.Errorf("cred for %q = {DBName:%q DBUser:%q}, want namespaced %q", srcName, cred.DBName, cred.DBUser, wantDest)
		}
	}
	// Must NOT be keyed by the namespaced destination name (the old bug).
	if _, ok := res.Credentials["johnnyq_45635"]; ok {
		t.Errorf("res.Credentials keyed by namespaced name johnnyq_45635 — the rewriters look up by source name and would miss")
	}
}

func keysOf(m map[string]DBCredential) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
