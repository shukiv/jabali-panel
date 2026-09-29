package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	planQueryAliasesRe   = regexp.MustCompile(`"queryEmailAliases": "([^"]*)"`)
	shQueryLoginRe       = regexp.MustCompile(`local query_login="([^"]*)"`)
	shQueryAliasesRe     = regexp.MustCompile(`local query_aliases="([^"]*)"`)
)

// directoryAliasQueries returns queryEmailAliases as apply-plan.json.tmpl and
// install.sh's converger carry it.
func directoryAliasQueries(t *testing.T) (plan, sh string) {
	t.Helper()
	root := repoRootT(t)
	planRaw, err := os.ReadFile(filepath.Join(root, "install", "stalwart", "apply-plan.json.tmpl"))
	if err != nil {
		t.Fatalf("read apply-plan.json.tmpl: %v", err)
	}
	shRaw, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	m := planQueryAliasesRe.FindSubmatch(planRaw)
	if m == nil {
		t.Fatal("apply-plan.json.tmpl: no queryEmailAliases")
	}
	n := shQueryAliasesRe.FindSubmatch(shRaw)
	if n == nil {
		t.Fatal("install.sh: no local query_aliases=\"...\"")
	}
	return string(m[1]), string(n[1])
}

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
		`CREATE TABLE domains (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL,
			email_enabled INTEGER NOT NULL DEFAULT 1, is_panel_primary INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE mailboxes (id TEXT PRIMARY KEY, domain_id TEXT NOT NULL, email_cached TEXT NOT NULL,
			password_hash TEXT NOT NULL, is_disabled INTEGER NOT NULL DEFAULT 0, send_only INTEGER NOT NULL DEFAULT 0,
			local_part TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE mail_groups (id TEXT PRIMARY KEY, email_cached TEXT, has_mailbox INTEGER, group_kind TEXT, internal_only INTEGER)`,
		`CREATE TABLE mail_group_members (group_id TEXT, mailbox_id TEXT)`,
		`CREATE TABLE email_forwarders (id TEXT, domain_id TEXT, mailbox_id TEXT, enabled INTEGER, type TEXT, local_part TEXT)`,
		`INSERT INTO users VALUES ('u-active', 0), ('u-suspended', 1)`,
		`INSERT INTO domains (id, user_id, name) VALUES ('d-active', 'u-active', 'active.test'), ('d-suspended', 'u-suspended', 'suspended.test')`,
		`INSERT INTO mailboxes (id, domain_id, email_cached, password_hash, is_disabled, send_only, local_part) VALUES
			('m1', 'd-active', 'alice@active.test', 'h', 0, 0, 'alice'),
			('m2', 'd-active', 'off@active.test', 'h', 1, 0, 'off'),
			('m3', 'd-suspended', 'carol@suspended.test', 'h', 0, 0, 'carol'),
			('m4', 'd-gone', 'orphan@gone.test', 'h', 0, 0, 'orphan')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v\n%s", err, stmt)
		}
	}
	return db
}

func directoryRows(t *testing.T, db *sql.DB, query, lookup string) int {
	t.Helper()
	return len(directoryEmails(t, db, query, lookup))
}

// directoryEmails returns the principal each row resolves to (the first column).
func directoryEmails(t *testing.T, db *sql.DB, query, lookup string) []string {
	t.Helper()
	rows, err := db.Query(query, lookup)
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, query)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var email, hash string
		if err := rows.Scan(&email, &hash); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, email)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
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

// RFC 5321 requires every mail domain to accept postmaster@. Stalwart answers
// 550 for an address the directory does not resolve, so DMARC and TLS reports
// sent to postmaster@<domain> never arrived. A domain that has no postmaster
// of its own resolves postmaster@ to the postmaster mailbox on the panel's
// primary domain (the server admin). Migration 000306 stops tenants making a
// postmaster@ now; a postmaster mailbox, alias or group a domain had before
// keeps its mail.
func TestStalwartQueryRecipient_PostmasterFallsBackToTheServerAdmin(t *testing.T) {
	_, _, planRecipient := directoryQueries(t)
	db := directoryTestDB(t)
	for _, stmt := range []string{
		`INSERT INTO domains (id, user_id, name, email_enabled, is_panel_primary) VALUES
			('d-panel', 'u-active', 'panel.test', 1, 1),
			('d-own', 'u-active', 'own.test', 1, 0),
			('d-alias', 'u-active', 'alias.test', 1, 0),
			('d-fwd', 'u-active', 'fwd.test', 1, 0),
			('d-group', 'u-active', 'group.test', 1, 0),
			('d-web', 'u-active', 'web.test', 0, 0)`,
		`INSERT INTO mailboxes (id, domain_id, email_cached, password_hash, local_part) VALUES
			('pm-admin', 'd-panel', 'postmaster@panel.test', 'h', 'postmaster'),
			('pm-own', 'd-own', 'postmaster@own.test', 'h', 'postmaster'),
			('m-alias', 'd-alias', 'boss@alias.test', 'h', 'boss'),
			('m-group', 'd-group', 'member@group.test', 'h', 'member')`,
		`INSERT INTO email_forwarders VALUES
			('f1', 'd-alias', 'm-alias', 1, 'alias', 'postmaster'),
			('f2', 'd-fwd', 'm-alias', 0, 'alias', 'postmaster')`,
		`INSERT INTO mail_groups VALUES ('g1', 'postmaster@group.test', 1, 'resource', 0)`,
		`INSERT INTO mail_group_members VALUES ('g1', 'm-group')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}
	cases := map[string][]string{
		"postmaster@active.test":    {"postmaster@panel.test"}, // no postmaster of its own
		"postmaster@suspended.test": {"postmaster@panel.test"},
		"postmaster@panel.test":     {"postmaster@panel.test"},
		"postmaster@own.test":       {"postmaster@own.test"}, // the domain's own mailbox keeps it
		"postmaster@alias.test":     {"boss@alias.test"},     // the domain's own alias keeps it
		"postmaster@group.test":     {"member@group.test"},   // the domain's own group keeps it
		"postmaster@fwd.test":       nil,                     // tenant's disabled alias: the tenant owns the name
		"postmaster@web.test":       nil,                     // email not enabled
		"postmaster@unknown.test":   nil,                     // not a panel domain
		"abuse@active.test":         nil,                     // only postmaster falls back
	}
	for lookup, want := range cases {
		got := directoryEmails(t, db, planRecipient, lookup)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("queryRecipient(%s) = %v, want %v", lookup, got, want)
		}
	}

	// A disabled admin postmaster mailbox receives nothing, for any domain.
	if _, err := db.Exec(`UPDATE mailboxes SET is_disabled = 1 WHERE id = 'pm-admin'`); err != nil {
		t.Fatal(err)
	}
	if got := directoryEmails(t, db, planRecipient, "postmaster@active.test"); len(got) != 0 {
		t.Errorf("queryRecipient(postmaster@active.test) with the admin postmaster disabled = %v, want none", got)
	}
}

// Stalwart delivers to an account only the addresses it owns: postmaster@ of a
// domain that falls back to the admin postmaster mailbox is accepted at RCPT
// but bounces "Mailbox not found" at delivery unless queryEmailAliases lists
// it on that mailbox (seen on the .60 test box, 2026-09-29).
func TestStalwartQueryEmailAliases_AdminPostmasterOwnsTheFallbackAddresses(t *testing.T) {
	planAliases, shAliases := directoryAliasQueries(t)
	if planAliases != shAliases {
		t.Fatalf("queryEmailAliases drift — edit BOTH:\n apply-plan: %s\n install.sh: %s", planAliases, shAliases)
	}
	db := directoryTestDB(t)
	for _, stmt := range []string{
		`INSERT INTO domains (id, user_id, name, email_enabled, is_panel_primary) VALUES
			('d-panel', 'u-active', 'panel.test', 1, 1),
			('d-own', 'u-active', 'own.test', 1, 0),
			('d-alias', 'u-active', 'alias.test', 1, 0),
			('d-group', 'u-active', 'group.test', 1, 0),
			('d-web', 'u-active', 'web.test', 0, 0)`,
		`INSERT INTO mailboxes (id, domain_id, email_cached, password_hash, local_part) VALUES
			('pm-admin', 'd-panel', 'postmaster@panel.test', 'h', 'postmaster'),
			('pm-own', 'd-own', 'postmaster@own.test', 'h', 'postmaster'),
			('m-alias', 'd-alias', 'boss@alias.test', 'h', 'boss')`,
		`INSERT INTO email_forwarders VALUES
			('f1', 'd-alias', 'm-alias', 1, 'alias', 'postmaster'),
			('f2', 'd-alias', 'm-alias', 1, 'alias', 'sales')`,
		`INSERT INTO mail_groups VALUES ('g1', 'postmaster@group.test', 1, 'resource', 0)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}
	cases := map[string][]string{
		// Every email-enabled domain without its own postmaster.
		"postmaster@panel.test": {"postmaster@active.test", "postmaster@suspended.test"},
		// A tenant's aliases are unchanged.
		"boss@alias.test":     {"postmaster@alias.test", "sales@alias.test"},
		"postmaster@own.test": nil,
		"alice@active.test":   nil,
	}
	for owner, want := range cases {
		got := directoryAliases(t, db, planAliases, owner)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("queryEmailAliases(%s) = %v, want %v", owner, got, want)
		}
	}
}

func directoryAliases(t *testing.T, db *sql.DB, query, owner string) []string {
	t.Helper()
	rows, err := db.Query(query, owner)
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, query)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, alias)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// An alias at an address a mailbox also holds is ignored: the mailbox gets
// its own mail, and the alias owner's account is not handed the address.
// Stalwart copies every alias it is given into its registry and resolves an
// address there first, so an alias returned here would sign the mailbox in
// to the alias owner's account.
func TestStalwartDirectory_MailboxWinsOverAliasAtItsAddress(t *testing.T) {
	_, _, planRecipient := directoryQueries(t)
	planAliases, _ := directoryAliasQueries(t)
	db := directoryTestDB(t)
	for _, stmt := range []string{
		`INSERT INTO mailboxes (id, domain_id, email_cached, password_hash, local_part) VALUES ('m5', 'd-active', 'info@active.test', 'h', 'info')`,
		`INSERT INTO email_forwarders VALUES
			('f1', 'd-active', 'm1', 1, 'alias', 'info'),
			('f2', 'd-active', 'm1', 1, 'alias', 'sales')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	if got := directoryEmails(t, db, planRecipient, "info@active.test"); strings.Join(got, ",") != "info@active.test" {
		t.Errorf("queryRecipient(info@active.test) = %v, want only the info@ mailbox", got)
	}
	if got := directoryEmails(t, db, planRecipient, "sales@active.test"); strings.Join(got, ",") != "alice@active.test" {
		t.Errorf("queryRecipient(sales@active.test) = %v, want alice (the alias still works)", got)
	}
	if got := directoryAliases(t, db, planAliases, "alice@active.test"); strings.Join(got, ",") != "sales@active.test" {
		t.Errorf("queryEmailAliases(alice@active.test) = %v, want only sales@ (info@ is a mailbox)", got)
	}
}
