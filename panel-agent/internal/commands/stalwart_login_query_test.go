package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

var (
	planDirectoryQueryRe = regexp.MustCompile(`"query(?:Login|Recipient|EmailAliases)": "([^"]*)"`)
	shDirectoryQueryRe   = regexp.MustCompile(`local query_(?:login|recipient|aliases)="([^"]*)"`)
	shStalwartGrantRe    = regexp.MustCompile(`GRANT SELECT(?: \(([^)]*)\))? ON jabali_panel\.(\w+)\s+TO '\$\{stalwart_db_user\}'`)
	sqlTableRe           = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+(\w+)(?:\s+(\w+))?`)
	sqlColumnRefRe       = regexp.MustCompile(`\b(\w+)\.(\w+)\b`)
	sqlKeywords          = map[string]bool{"WHERE": true, "ON": true, "JOIN": true, "LEFT": true, "INNER": true, "UNION": true, "GROUP": true, "ORDER": true, "LIMIT": true, "AS": true}
)

// Stalwart reads the directory as jabali-stalwart-ro, which install.sh grants
// SELECT on named tables only. A query that reads a table, or under a
// column-level grant a column, that the user cannot read fails on every
// lookup: for queryLogin that is every mail login on the box. users is granted
// by column only, because it also holds password hashes and the encrypted
// database admin passwords.
func TestStalwartDirectoryQueries_ReadOnlyGrantedTables(t *testing.T) {
	root := repoRootT(t)
	sh, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	plan, err := os.ReadFile(filepath.Join(root, "install", "stalwart", "apply-plan.json.tmpl"))
	if err != nil {
		t.Fatalf("read apply-plan.json.tmpl: %v", err)
	}

	grants := map[string]map[string]bool{} // table -> granted columns, nil = the whole table
	for _, m := range shStalwartGrantRe.FindAllSubmatch(sh, -1) {
		table := string(m[2])
		if len(m[1]) == 0 {
			grants[table] = nil
			continue
		}
		cols := map[string]bool{}
		for _, c := range strings.Split(string(m[1]), ",") {
			cols[strings.TrimSpace(c)] = true
		}
		grants[table] = cols
	}
	if len(grants) == 0 {
		t.Fatal("install.sh: no GRANT SELECT ... TO '${stalwart_db_user}' found")
	}
	if cols, ok := grants["users"]; ok && cols == nil {
		t.Error("install.sh grants jabali-stalwart-ro the whole users table; grant only the columns the directory reads")
	}

	var queries []string
	for _, m := range shDirectoryQueryRe.FindAllSubmatch(sh, -1) {
		queries = append(queries, string(m[1]))
	}
	for _, m := range planDirectoryQueryRe.FindAllSubmatch(plan, -1) {
		queries = append(queries, string(m[1]))
	}
	if len(queries) != 6 {
		t.Fatalf("found %d directory queries, want 3 in install.sh and 3 in apply-plan.json.tmpl", len(queries))
	}

	for _, query := range queries {
		aliases := map[string]string{}
		for _, m := range sqlTableRe.FindAllStringSubmatch(query, -1) {
			table, alias := m[1], m[2]
			if _, ok := grants[table]; !ok {
				t.Errorf("a directory query reads %s, which install.sh does not grant jabali-stalwart-ro:\n%s", table, query)
				continue
			}
			if alias == "" || sqlKeywords[strings.ToUpper(alias)] {
				alias = table
			}
			aliases[alias] = table
		}
		for _, m := range sqlColumnRefRe.FindAllStringSubmatch(query, -1) {
			table, ok := aliases[m[1]]
			if !ok {
				continue
			}
			if cols := grants[table]; cols != nil && !cols[m[2]] {
				t.Errorf("a directory query reads %s.%s, which install.sh does not grant jabali-stalwart-ro:\n%s", table, m[2], query)
			}
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
