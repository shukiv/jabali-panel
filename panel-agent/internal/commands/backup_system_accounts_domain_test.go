package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: when a system restore rebuilds an account the panel's database
// doesn't have, its domains come back with every setting the account backup
// carries, through the same INSERT the agent runs as MariaDB's root.

// captureDomainInsert runs insertDomain for d and returns the statement it
// sent to mariadb.
func captureDomainInsert(t *testing.T, d backup.MetadataDomain) string {
	t.Helper()
	var stmt string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		for i, a := range args {
			if name == "mariadb" && a == "-e" && i+1 < len(args) {
				stmt = args[i+1]
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	if err := insertDomain(context.Background(), "u1", d); err != nil {
		t.Fatal(err)
	}
	if stmt == "" {
		t.Fatal("insertDomain sent no statement")
	}
	return stmt
}

// mariaInsert reads an INSERT ... (columns) VALUES (values) statement the way
// MariaDB's lexer does with the default sql_mode: inside a quoted string a
// backslash escapes the next character, and a doubled quote is one quote. It
// returns each column's value, the columns, and what follows the VALUES list.
func mariaInsert(t *testing.T, stmt string) (map[string]string, []string, string) {
	t.Helper()
	i, j := strings.Index(stmt, "("), strings.Index(stmt, ") VALUES (")
	if i < 0 || j < i {
		t.Fatalf("not an INSERT with a column list: %s", stmt)
	}
	var cols []string
	for _, c := range strings.Split(stmt[i+1:j], ",") {
		cols = append(cols, strings.TrimSpace(c))
	}
	rest := stmt[j+len(") VALUES ("):]
	var vals []string
	var cur strings.Builder
	depth, inStr := 0, false
	for k := 0; k < len(rest); k++ {
		c := rest[k]
		if inStr {
			cur.WriteByte(c)
			switch {
			case c == '\\' && k+1 < len(rest):
				k++
				cur.WriteByte(rest[k])
			case c == '\'' && k+1 < len(rest) && rest[k+1] == '\'':
				k++
				cur.WriteByte('\'')
			case c == '\'':
				inStr = false
			}
			continue
		}
		switch {
		case c == '\'':
			inStr = true
			cur.WriteByte(c)
		case c == '(':
			depth++
			cur.WriteByte(c)
		case c == ')' && depth == 0:
			vals = append(vals, strings.TrimSpace(cur.String()))
			if len(vals) != len(cols) {
				t.Fatalf("%d columns, %d values: %s", len(cols), len(vals), stmt)
			}
			out := map[string]string{}
			for n, c := range cols {
				out[c] = vals[n]
			}
			return out, cols, rest[k+1:]
		case c == ')':
			depth--
			cur.WriteByte(c)
		case c == ',' && depth == 0:
			vals = append(vals, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	t.Fatalf("the VALUES list never closes: %s", stmt)
	return nil, nil, ""
}

// mariaString is the text a quoted literal stands for, read as mariaInsert
// reads it.
func mariaString(t *testing.T, lit string) string {
	t.Helper()
	if len(lit) < 2 || lit[0] != '\'' || lit[len(lit)-1] != '\'' {
		t.Fatalf("not a string literal: %s", lit)
	}
	esc := map[byte]byte{'n': '\n', 't': '\t', 'r': '\r', '0': 0}
	s := lit[1 : len(lit)-1]
	var b strings.Builder
	for k := 0; k < len(s); k++ {
		switch {
		case s[k] == '\\' && k+1 < len(s):
			k++
			if m, ok := esc[s[k]]; ok {
				b.WriteByte(m)
			} else {
				b.WriteByte(s[k])
			}
		case s[k] == '\'' && k+1 < len(s) && s[k+1] == '\'':
			k++
			b.WriteByte('\'')
		default:
			b.WriteByte(s[k])
		}
	}
	return b.String()
}

func dsPtr[T any](v T) *T { return &v }

// Every setting the backup carries for a domain has its column in the
// INSERT. A field added to MetadataDomain without one fails here.
func TestInsertDomain_EveryBackupSettingHasAColumn(t *testing.T) {
	// Not settings of the row: a marker of the archive. The rows that hang
	// off the domain (aliases, certificate, mailboxes, ...) are slices or
	// structs and are skipped below.
	notColumns := map[string]bool{"php_settings_complete": true}
	row, _, _ := mariaInsert(t, captureDomainInsert(t, backup.MetadataDomain{ID: "d1", Name: "alice.org"}))
	typ := reflect.TypeOf(backup.MetadataDomain{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		k := f.Type.Kind()
		if k == reflect.Slice || (k == reflect.Ptr && f.Type.Elem().Kind() == reflect.Struct) {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if notColumns[name] {
			continue
		}
		if _, ok := row[name]; !ok {
			t.Errorf("MetadataDomain.%s (%s) has no column in the domain INSERT", f.Name, name)
		}
	}
}

// A tenant's text stays inside its own literal, whatever quotes and
// backslashes it holds: the INSERT runs as MariaDB's root.
func TestInsertDomain_TenantTextStaysInsideItsLiteral(t *testing.T) {
	payload := `x\', 1); DROP TABLE jabali_panel.users; -- \\' '' \`
	env, _ := json.Marshal(map[string]string{"A": payload})
	d := backup.MetadataDomain{
		ID: "d1", Name: "alice.org", DocRoot: payload,
		DisclaimerText: dsPtr(payload), NginxTenantDirectives: dsPtr(payload),
		PHPOpenBasedir: dsPtr(payload), CalDAVHost: payload, EnvVars: string(env),
	}
	row, _, after := mariaInsert(t, captureDomainInsert(t, d))
	if strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(after), ";")) != "" {
		t.Fatalf("the statement goes on after its VALUES list: %q", after)
	}
	for _, col := range []string{"doc_root", "disclaimer_text", "nginx_tenant_directives", "php_open_basedir", "caldav_host"} {
		if got := mariaString(t, row[col]); got != payload {
			t.Errorf("%s reads back as %q, want %q", col, got, payload)
		}
	}
	if got := mariaString(t, row["env_vars"]); got != string(env) {
		t.Errorf("env_vars reads back as %q, want %q", got, env)
	}
}

func TestSQLEscape_BackslashAndQuote(t *testing.T) {
	if got := sqlEscape(`a\'b`); got != `a\\''b` {
		t.Fatalf("sqlEscape = %q", got)
	}
}

// The settings come back as recorded. A setting an older archive doesn't
// carry takes the column's default, as a domain the panel creates does.
func TestInsertDomain_RestoresTheDomainsSettings(t *testing.T) {
	d := backup.MetadataDomain{
		ID: "d1", Name: "alice.org",
		PHPDisplayErrors: dsPtr(true), PHPErrorReporting: dsPtr(22527), PHPTimezone: dsPtr("Asia/Jerusalem"),
		PHPLogErrors: dsPtr(true), PHPFileUploads: dsPtr(false), PHPShortOpenTag: dsPtr(true),
		PHPAllowURLFopen: dsPtr(false), PHPSettingsComplete: true,
		SSLMode: "self", SkipAutoSAN: true,
		MailProvider: "m365", M365Onmicrosoft: dsPtr("alice"), GoogleDKIM: dsPtr("v=DKIM1; k=rsa; p=AAA"),
		DmarcNP: "reject", DmarcTesting: true, CardDAVHost: "dav.alice.org", MTASTSEnabled: true,
		NginxSafeOptions: `{"client_max_body_size":"64m"}`, CacheEnabled: true, CachePath: "/blog",
		CacheTTLSeconds: 300, CacheQueryAllowlist: "page", CreateWWW: dsPtr(false), WebmailEnabled: dsPtr(false),
		TempURLEnabled: true, BotChallengeExempt: true, BotChallengeInclude: true,
		AllowSubdomainDelegation: true, WebDisabled: true, DNSDisabled: true,
	}
	row, _, _ := mariaInsert(t, captureDomainInsert(t, d))
	want := map[string]string{
		"php_display_errors": "1", "php_error_reporting": "22527", "php_timezone": "'Asia/Jerusalem'",
		"php_log_errors": "1", "php_file_uploads": "0", "php_short_open_tag": "1", "php_allow_url_fopen": "0",
		"ssl_mode": "'self'", "skip_auto_san": "1",
		"mail_provider": "'m365'", "m365_onmicrosoft": "'alice'", "google_dkim": "'v=DKIM1; k=rsa; p=AAA'",
		"dmarc_np": "'reject'", "dmarc_testing": "1", "caldav_host": "''", "carddav_host": "'dav.alice.org'",
		// Its policy's DNS records are published by the panel's restore,
		// which this one isn't: it is turned on in the mail settings after.
		"mta_sts_enabled":    "0",
		"nginx_safe_options": `'{"client_max_body_size":"64m"}'`, "env_vars": "DEFAULT",
		"cache_enabled": "1", "cache_path": "'/blog'", "cache_ttl_seconds": "300", "cache_query_allowlist": "'page'",
		"create_www": "0", "webmail_enabled": "0", "temp_url_enabled": "1", "bot_challenge_exempt": "1",
		"bot_challenge_include": "1", "allow_subdomain_delegation": "1", "web_disabled": "1", "dns_disabled": "1",
	}
	for col, v := range want {
		if row[col] != v {
			t.Errorf("%s = %s, want %s", col, row[col], v)
		}
	}

	old, _, _ := mariaInsert(t, captureDomainInsert(t, backup.MetadataDomain{ID: "d2", Name: "old.org", SSLMode: "bogus", EnvVars: "{not json"}))
	for _, col := range []string{"ssl_mode", "mail_provider", "cache_path", "cache_ttl_seconds", "create_www", "webmail_enabled", "env_vars", "nginx_safe_options"} {
		if old[col] != "DEFAULT" {
			t.Errorf("an archive without %s: %s, want DEFAULT", col, old[col])
		}
	}
	for _, col := range []string{"php_display_errors", "php_timezone", "php_open_basedir", "php_allow_url_fopen"} {
		if old[col] != "NULL" {
			t.Errorf("an archive without %s: %s, want NULL (inherit)", col, old[col])
		}
	}
}

// Ownership follows the panel's restore rule: verified with method restore,
// unless the backup records the name as pending. Either way the row gets a
// fresh challenge token.
func TestInsertDomain_Ownership(t *testing.T) {
	hex64 := regexp.MustCompile(`^'[0-9a-f]{64}'$`)
	for _, status := range []string{"", "verified"} {
		row, _, _ := mariaInsert(t, captureDomainInsert(t, backup.MetadataDomain{ID: "d1", Name: "alice.org", OwnershipStatus: status}))
		if row["ownership_status"] != "'verified'" || row["ownership_method"] != "'restore'" ||
			row["ownership_verified_at"] != "UTC_TIMESTAMP(6)" || row["ownership_last_result"] != "'verified'" ||
			!hex64.MatchString(row["ownership_token"]) {
			t.Fatalf("status %q: ownership %s/%s at %s result %s token %s", status, row["ownership_status"],
				row["ownership_method"], row["ownership_verified_at"], row["ownership_last_result"], row["ownership_token"])
		}
	}
	row, _, _ := mariaInsert(t, captureDomainInsert(t, backup.MetadataDomain{ID: "d1", Name: "alice.org", OwnershipStatus: "pending"}))
	if row["ownership_status"] != "'pending'" || row["ownership_method"] != "''" ||
		row["ownership_pending_since"] != "UTC_TIMESTAMP(6)" || row["ownership_next_check_at"] != "UTC_TIMESTAMP(6)" ||
		!hex64.MatchString(row["ownership_token"]) {
		t.Fatalf("pending: %v", row)
	}
	a, _, _ := mariaInsert(t, captureDomainInsert(t, backup.MetadataDomain{ID: "d1", Name: "alice.org"}))
	b, _, _ := mariaInsert(t, captureDomainInsert(t, backup.MetadataDomain{ID: "d2", Name: "bob.org"}))
	if a["ownership_token"] == b["ownership_token"] {
		t.Fatal("two domains got the same ownership token")
	}
}

// Each switch lands in its own column: with only one of them on, only its
// column is 1.
func TestInsertDomain_EachSwitchInItsOwnColumn(t *testing.T) {
	switches := map[string]func(*backup.MetadataDomain){
		"is_enabled":                 func(d *backup.MetadataDomain) { d.IsEnabled = true },
		"ssl_enabled":                func(d *backup.MetadataDomain) { d.SSLEnabled = true },
		"email_enabled":              func(d *backup.MetadataDomain) { d.EmailEnabled = true },
		"is_panel_primary":           func(d *backup.MetadataDomain) { d.IsPanelPrimary = true },
		"disclaimer_enabled":         func(d *backup.MetadataDomain) { d.DisclaimerEnabled = true },
		"dnssec_enabled":             func(d *backup.MetadataDomain) { d.DNSSECEnabled = true },
		"php_display_errors":         func(d *backup.MetadataDomain) { d.PHPDisplayErrors = dsPtr(true) },
		"php_log_errors":             func(d *backup.MetadataDomain) { d.PHPLogErrors = dsPtr(true) },
		"php_file_uploads":           func(d *backup.MetadataDomain) { d.PHPFileUploads = dsPtr(true) },
		"php_short_open_tag":         func(d *backup.MetadataDomain) { d.PHPShortOpenTag = dsPtr(true) },
		"php_allow_url_fopen":        func(d *backup.MetadataDomain) { d.PHPAllowURLFopen = dsPtr(true) },
		"skip_auto_san":              func(d *backup.MetadataDomain) { d.SkipAutoSAN = true },
		"dmarc_testing":              func(d *backup.MetadataDomain) { d.DmarcTesting = true },
		"cache_enabled":              func(d *backup.MetadataDomain) { d.CacheEnabled = true },
		"create_www":                 func(d *backup.MetadataDomain) { d.CreateWWW = dsPtr(true) },
		"webmail_enabled":            func(d *backup.MetadataDomain) { d.WebmailEnabled = dsPtr(true) },
		"temp_url_enabled":           func(d *backup.MetadataDomain) { d.TempURLEnabled = true },
		"bot_challenge_exempt":       func(d *backup.MetadataDomain) { d.BotChallengeExempt = true },
		"bot_challenge_include":      func(d *backup.MetadataDomain) { d.BotChallengeInclude = true },
		"allow_subdomain_delegation": func(d *backup.MetadataDomain) { d.AllowSubdomainDelegation = true },
		"web_disabled":               func(d *backup.MetadataDomain) { d.WebDisabled = true },
		"dns_disabled":               func(d *backup.MetadataDomain) { d.DNSDisabled = true },
	}
	for on, set := range switches {
		d := backup.MetadataDomain{ID: "d1", Name: "alice.org", CreateWWW: dsPtr(false), WebmailEnabled: dsPtr(false)}
		set(&d)
		row, _, _ := mariaInsert(t, captureDomainInsert(t, d))
		for col := range switches {
			if got, want := row[col] == "1", col == on; got != want {
				t.Errorf("only %s on: %s = %s", on, col, row[col])
			}
		}
	}
}
