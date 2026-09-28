package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	_ "modernc.org/sqlite"
)

// Stalwart's SQL directory decides who may sign in (queryLogin) and who
// receives mail (queryRecipient). Both live in install/stalwart/apply-plan.json.tmpl
// (fresh installs) and in install.sh's ADR-0073 converger, which overwrites the
// live fields on every install and update ([[feedback_applyplan_converger_drift]]).
//
// A suspended user's mailboxes must stop signing in (IMAP, POP3, SMTP
// submission, JMAP, webmail) and keep receiving: suspension is not deletion.
// These tests run the real queries, as the two files carry them, against a
// small in-memory schema.

var (
	planQueryLoginRe     = regexp.MustCompile(`"queryLogin": "([^"]*)"`)
	planQueryRecipientRe = regexp.MustCompile(`"queryRecipient": "([^"]*)"`)
	shQueryLoginRe       = regexp.MustCompile(`local query_login="([^"]*)"`)
)

func directoryQueries(t *testing.T) (planLogin, shLogin, planRecipient string) {
	t.Helper()
	root := repoRootT(t)
	plan, err := os.ReadFile(filepath.Join(root, "install", "stalwart", "apply-plan.json.tmpl"))
	if err != nil {
		t.Fatalf("read apply-plan.json.tmpl: %v", err)
	}
	sh, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	m := planQueryLoginRe.FindSubmatch(plan)
	if m == nil {
		t.Fatal("apply-plan.json.tmpl: no queryLogin")
	}
	planLogin = string(m[1])
	m = shQueryLoginRe.FindSubmatch(sh)
	if m == nil {
		t.Fatal("install.sh: no local query_login=\"...\" in the directory converger — queryLogin would never reach an existing box")
	}
	shLogin = string(m[1])
	m = planQueryRecipientRe.FindSubmatch(plan)
	if m == nil {
		t.Fatal("apply-plan.json.tmpl: no queryRecipient")
	}
	planRecipient = string(m[1])
	return planLogin, shLogin, planRecipient
}

func directoryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1) // in-memory sqlite: one connection is one database
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, suspended INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE domains (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE mailboxes (id TEXT PRIMARY KEY, domain_id TEXT NOT NULL, email_cached TEXT NOT NULL,
			password_hash TEXT NOT NULL, is_disabled INTEGER NOT NULL DEFAULT 0, send_only INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE mail_groups (id TEXT PRIMARY KEY, email_cached TEXT, has_mailbox INTEGER, group_kind TEXT, internal_only INTEGER)`,
		`CREATE TABLE mail_group_members (group_id TEXT, mailbox_id TEXT)`,
		`CREATE TABLE email_forwarders (id TEXT, domain_id TEXT, mailbox_id TEXT, enabled INTEGER, type TEXT, local_part TEXT)`,
		`INSERT INTO users VALUES ('u-active', 0), ('u-suspended', 1)`,
		`INSERT INTO domains VALUES ('d-active', 'u-active', 'active.test'), ('d-suspended', 'u-suspended', 'suspended.test')`,
		`INSERT INTO mailboxes VALUES
			('m1', 'd-active', 'alice@active.test', 'h', 0, 0),
			('m2', 'd-active', 'off@active.test', 'h', 1, 0),
			('m3', 'd-suspended', 'carol@suspended.test', 'h', 0, 0),
			('m4', 'd-gone', 'orphan@gone.test', 'h', 0, 0)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v\n%s", err, stmt)
		}
	}
	return db
}

func directoryRows(t *testing.T, db *sql.DB, query, lookup string) int {
	t.Helper()
	rows, err := db.Query(query, lookup)
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, query)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return n
}

// The converger is what runs on an existing box, so the two copies must match,
// and the converger must send queryLogin, not only define it.
func TestStalwartQueryLogin_InstallMatchesApplyPlan(t *testing.T) {
	planLogin, shLogin, _ := directoryQueries(t)
	if planLogin != shLogin {
		t.Fatalf("queryLogin drift — edit BOTH:\n apply-plan: %s\n install.sh: %s", planLogin, shLogin)
	}
	sh, err := os.ReadFile(filepath.Join(repoRootT(t), "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	patch := regexp.MustCompile(`patch_json="\$\(python3 -c '[^']*"queryLogin": sys\.argv\[(\d)\][^']*' ([^)]*)\)"`).FindSubmatch(sh)
	if patch == nil {
		t.Fatal("install.sh: the directory converger's patch_json does not send queryLogin")
	}
	args := regexp.MustCompile(`"\$[a-z_]+"`).FindAll(patch[2], -1)
	i := int(patch[1][0] - '1')
	if i < 0 || i >= len(args) || string(args[i]) != `"$query_login"` {
		t.Fatalf("install.sh: patch_json sends queryLogin from %q, want \"$query_login\"", args)
	}
}

func TestStalwartQueryLogin_SuspendedOwnerCannotSignIn(t *testing.T) {
	planLogin, _, _ := directoryQueries(t)
	db := directoryTestDB(t)
	cases := map[string]int{
		"alice@active.test":    1, // enabled mailbox, active owner
		"off@active.test":      0, // disabled mailbox
		"carol@suspended.test": 0, // owner suspended
		"orphan@gone.test":     1, // no domain row: not locked out by a missing join
		"nobody@active.test":   0,
	}
	for email, want := range cases {
		if got := directoryRows(t, db, planLogin, email); got != want {
			t.Errorf("queryLogin(%s) = %d rows, want %d", email, got, want)
		}
	}
}

// Suspension keeps the mail: a suspended user's mailbox still receives.
func TestStalwartQueryRecipient_SuspendedOwnerStillReceives(t *testing.T) {
	_, _, planRecipient := directoryQueries(t)
	db := directoryTestDB(t)
	if got := directoryRows(t, db, planRecipient, "carol@suspended.test"); got != 1 {
		t.Fatalf("queryRecipient(carol@suspended.test) = %d rows, want 1: suspension must not bounce mail", got)
	}
	if got := directoryRows(t, db, planRecipient, "off@active.test"); got != 0 {
		t.Fatalf("queryRecipient(off@active.test) = %d rows, want 0 (disabled mailbox)", got)
	}
}
