package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH #723, round 2 — johnnyq re-tested on the shipped fix and it STILL rewrote
// his config password when he chose to carry passwords over. The earlier fix
// (#729 keying + JAB-207 KeepSourcePassword) only ever engaged when the
// destination jabali account was named the SAME as the source panel account —
// because the "keep the password" signal was gated on the source DB user name
// colliding with the panel-managed `<target>_<db>` user. johnnyq's real
// HestiaCP migrations name the new account differently from the source, so the
// collision never fired, --preserve-source-state was ignored by the config
// rewriter, and wp-config got a fresh temp password.
//
// The exact shape of johnnyq's backup: source account "notary", a WordPress DB
// `notary_45635` whose DB user is spelled the same (`notary_45635`, HestiaCP's
// `user_db` norm), migrated into a jabali account named DIFFERENTLY.
//
// johnnyq's actual wp-config.php DB block (verbatim):
const gh723PreserveWPConfig = `<?php
define( 'DB_NAME', 'notary_45635' );
define( 'DB_USER', 'notary_45635' );
define( 'DB_PASSWORD', 'e4eb22f97bb143151c95' );
define( 'DB_HOST', 'localhost' );
$table_prefix = 'wp_JV8O5_';
require_once ABSPATH . 'wp-settings.php';
`

// a recreatable mysql_native_password hash, as HestiaCP's db.conf MD5= carries.
const gh723NativeHash = "*0123456789ABCDEF0123456789ABCDEF01234567"

// TestGH723_Preserve_KeepsSourceCreds_WhenDestNameDiffersFromSource is the
// regression that four "fixed" posts missed: preserve ON, destination account
// ("sewickley") named differently from the source ("notary"), the source DB
// user recreated as a compat user. The migrated wp-config must keep its
// ORIGINAL user + password (the recreated user authenticates with them) and
// only have DB_NAME namespaced + DB_HOST normalised.
func TestGH723_Preserve_KeepsSourceCreds_WhenDestNameDiffersFromSource(t *testing.T) {
	extract := t.TempDir()
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
		// HestiaCP shape: one compat user per DB, spelled the same as the DB,
		// carrying the native password hash + a grant on its own DB.
		CompatUsers: []CompatUser{
			{Name: "notary_45635", Host: "localhost", Hash: gh723NativeHash,
				Grant: []CompatGrant{{SourceDB: "notary_45635", Privs: []string{"ALL"}}}},
			{Name: "notary_itflow", Host: "localhost", Hash: gh723NativeHash,
				Grant: []CompatGrant{{SourceDB: "notary_itflow", Privs: []string{"ALL"}}}},
		},
	}

	// preserve = TRUE, target account "sewickley" (differs from source "notary").
	res, err := ImportDatabases(
		context.Background(),
		gh723DBRepo{}, gh723DBUserRepo{}, gh723GrantRepo{},
		&recordingAgent{},
		parsed,
		"01USERULID0000000000000000", "sewickley", true,
	)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}

	// The compat pass must have marked the source user as preserved on BOTH DBs.
	for _, srcDB := range []string{"notary_45635", "notary_itflow"} {
		cred, ok := res.Credentials[srcDB]
		if !ok {
			t.Fatalf("res.Credentials missing %q (keys=%v)", srcDB, keysOf(res.Credentials))
		}
		if !cred.preservesUser(srcDB) {
			t.Errorf("cred[%q].PreservedUsers=%v — the recreated source user %q must be preserved",
				srcDB, cred.PreservedUsers, srcDB)
		}
	}

	// End to end: rewriting johnnyq's real wp-config with these credentials must
	// leave DB_USER + DB_PASSWORD exactly as the source wrote them.
	out, changed := rewriteWordPress(gh723PreserveWPConfig, res.Credentials)
	if !changed {
		t.Fatalf("wp-config was not rewritten at all (DB_NAME should still change):\n%s", out)
	}
	// DB_NAME namespaced, DB_HOST normalised.
	if !strings.Contains(out, "define('DB_NAME', 'sewickley_45635');") {
		t.Errorf("DB_NAME must be namespaced to the destination:\n%s", out)
	}
	if !strings.Contains(out, "define('DB_HOST', 'localhost');") {
		t.Errorf("DB_HOST must be normalised:\n%s", out)
	}
	// The crux: original user + password survive — kept VERBATIM, preserving
	// johnnyq's original line formatting (only DB_NAME/DB_HOST are normalised).
	if !strings.Contains(out, "define( 'DB_USER', 'notary_45635' );") {
		t.Errorf("GH #723: DB_USER must stay the source user on the preserve path:\n%s", out)
	}
	if !strings.Contains(out, "define( 'DB_PASSWORD', 'e4eb22f97bb143151c95' );") {
		t.Errorf("GH #723 REGRESSION: the source DB password was overwritten — this is the exact bug johnnyq re-reported:\n%s", out)
	}
	// And the namespaced destination user (panel-managed temp account) must NOT
	// have been written into the config — that account is not what the app auths as.
	if strings.Contains(out, "define('DB_USER', 'sewickley_45635')") {
		t.Errorf("DB_USER was rewritten to the panel-managed user despite preserve:\n%s", out)
	}
}

// TestGH723_Preserve_FallsBackToPanelCreds_WhenUserNotRecreated guards the
// degradation edge the advisor flagged: preserve is ON but the source user's
// hash format can't be recreated (ed25519 / auth socket / unsupported). The
// compat user is NOT created, so the config must fall back to the panel-managed
// credentials (which DO exist) rather than preserving creds that authenticate
// against nothing.
func TestGH723_Preserve_FallsBackToPanelCreds_WhenUserNotRecreated(t *testing.T) {
	extract := t.TempDir()
	if err := os.WriteFile(filepath.Join(extract, "notary_45635.mysql.sql"), []byte("-- dump\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed := &ParsedTarball{
		ExtractDir: extract,
		SourceUser: "notary",
		MySQLDumps: []string{filepath.Join(extract, "notary_45635.mysql.sql")},
		CompatUsers: []CompatUser{
			// NOT a native-password hash → IsNativePasswordHash false → skipped.
			{Name: "notary_45635", Host: "localhost", Hash: "invalid-not-a-native-hash",
				Grant: []CompatGrant{{SourceDB: "notary_45635", Privs: []string{"ALL"}}}},
		},
	}

	res, err := ImportDatabases(
		context.Background(),
		gh723DBRepo{}, gh723DBUserRepo{}, gh723GrantRepo{},
		&recordingAgent{},
		parsed,
		"01USERULID0000000000000000", "sewickley", true,
	)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}

	cred := res.Credentials["notary_45635"]
	if cred.preservesUser("notary_45635") {
		t.Fatalf("user with an unrecreatable hash must NOT be marked preserved: %v", cred.PreservedUsers)
	}

	out, _ := rewriteWordPress(gh723PreserveWPConfig, res.Credentials)
	// Falls back to the panel-managed rewrite: namespaced user, and the original
	// password is gone (replaced by the panel-managed temp password).
	if !strings.Contains(out, "define('DB_USER', 'sewickley_45635');") {
		t.Errorf("fallback must rewrite DB_USER to the panel-managed user:\n%s", out)
	}
	if strings.Contains(out, "define('DB_PASSWORD', 'e4eb22f97bb143151c95');") {
		t.Errorf("fallback must not keep a password no recreated user authenticates:\n%s", out)
	}
}
