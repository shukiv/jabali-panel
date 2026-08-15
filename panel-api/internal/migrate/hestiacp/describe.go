package hestiacp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// describe.go — per-area builders for the Hestia importer.
//
// **STATUS:** Coded against documented HestiaCP CLI JSON output
// (Hestia API docs + v-list-* shape per source repo). NOT
// validated against a live Hestia host. Field-set drift surfaces
// in the manifest's Warnings list; operator running against an
// unfamiliar Hestia minor inspects the warnings + files the
// actual shape for a follow-up parser tightening.
//
// Hestia commands (all admin-only, JSON-mode):
//   v-list-web-domains  <user> json   → web domains map
//   v-list-databases    <user> json   → mariadb DBs map
//   v-list-mail-domains <user> json   → mail domains map
//   v-list-mail-accounts <user> <domain> json → per-domain mailboxes

// hestiaDomain mirrors the v-list-web-domains JSON shape:
//
//	{ "<domain>": { "DOCUMENT_ROOT": "...", "ALIAS": "...",
//	                "TPL": "default", "IP": "...", "U_DISK": "...",
//	                "PHP_VER": "8.2", "SUSPENDED": "no", ... } }
type hestiaDomainAttrs struct {
	DocumentRoot string `json:"DOCUMENT_ROOT"`
	Alias        string `json:"ALIAS"`
	TPL          string `json:"TPL"`
	IP           string `json:"IP"`
	UDisk        string `json:"U_DISK"`
	PHPVer       string `json:"PHP_VER"`
	Suspended    string `json:"SUSPENDED"`
}

func (d *Discoverer) describeDomains(ctx context.Context, s *session, account string) ([]migrate.DomainSpec, error) {
	// HestiaCP renamed VestaCP's combined v-list-user-domains to the
	// web-specific v-list-web-domains (GH #327); the old name errors with
	// "is not a valid hestia command". Same {domain: {DOCUMENT_ROOT,…}} JSON.
	out, err := s.run(ctx, d.CommandTimeout,
		fmt.Sprintf("v-list-web-domains '%s' json",
			strings.ReplaceAll(account, "'", `'\''`)))
	if err != nil {
		return nil, fmt.Errorf("v-list-web-domains: %w", err)
	}
	var doms map[string]hestiaDomainAttrs
	if err := json.Unmarshal(out, &doms); err != nil {
		return nil, fmt.Errorf("v-list-web-domains decode: %w", err)
	}
	rows := []migrate.DomainSpec{}
	first := true
	for name, a := range doms {
		spec := migrate.DomainSpec{
			Name:    name,
			DocRoot: a.DocumentRoot,
			HasPHP:  a.PHPVer != "" && !strings.EqualFold(a.PHPVer, "no"),
			PHPVer:  a.PHPVer,
		}
		if spec.DocRoot == "" {
			spec.DocRoot = fmt.Sprintf(
				"/home/%s/web/%s/public_html", account, name)
		}
		// Hestia domain map iteration order isn't stable. We mark
		// the FIRST iterated domain as primary; per-account a
		// real 'main' domain isn't surfaced via v-list-web-
		// domains so this is best-effort. Operator can edit
		// is_panel_primary post-restore via panel UI.
		if first {
			spec.IsPrimary = true
			first = false
		}
		rows = append(rows, spec)
		// Aliases: Hestia stores alt-domains in ALIAS as comma
		// list. Each alias becomes its own DomainSpec sharing
		// the parent docroot — matches the cpanel parked-domain
		// layout already handled by the panel domain model.
		for _, al := range splitCSV(a.Alias) {
			rows = append(rows, migrate.DomainSpec{
				Name:    al,
				DocRoot: spec.DocRoot,
				HasPHP:  spec.HasPHP,
				PHPVer:  spec.PHPVer,
			})
		}
	}
	return rows, nil
}

// hestiaDB mirrors v-list-databases JSON:
//
//	{ "<dbname>": { "DATABASE": "...", "DBUSER": "...", "TYPE": "mysql",
//	                "U_DISK": "10", "SUSPENDED": "no" } }
type hestiaDBAttrs struct {
	Database  string `json:"DATABASE"`
	DBUser    string `json:"DBUSER"`
	Type      string `json:"TYPE"`
	UDisk     string `json:"U_DISK"`
	Suspended string `json:"SUSPENDED"`
}

func (d *Discoverer) describeDatabases(ctx context.Context, s *session, account string) ([]migrate.DatabaseSpec, []migrate.Warning, error) {
	out, err := s.run(ctx, d.CommandTimeout,
		fmt.Sprintf("v-list-databases '%s' json",
			strings.ReplaceAll(account, "'", `'\''`)))
	if err != nil {
		return nil, []migrate.Warning{{
			Code:   "hestiacp_v_list_databases_failed",
			Detail: fmt.Sprintf("%v", err),
			At:     time.Now().UTC(),
		}}, nil
	}
	var dbs map[string]hestiaDBAttrs
	if err := json.Unmarshal(out, &dbs); err != nil {
		return nil, nil, fmt.Errorf("v-list-databases decode: %w", err)
	}
	specs := []migrate.DatabaseSpec{}
	warnings := []migrate.Warning{}
	for name, a := range dbs {
		engine := strings.ToLower(a.Type)
		if engine == "" {
			engine = "mysql"
		}
		if engine == "pgsql" || engine == "postgres" {
			warnings = append(warnings, migrate.Warning{
				Code:   "postgres_unsupported",
				Detail: fmt.Sprintf("PostgreSQL DB %q skipped — re-import after M37 integration ships", name),
				At:     time.Now().UTC(),
			})
			continue
		}
		specs = append(specs, migrate.DatabaseSpec{
			Engine:    "mysql",
			Name:      name,
			GrantUser: a.DBUser,
			Bytes:     parseHestiaMB(a.UDisk),
		})
	}
	return specs, warnings, nil
}

// hestiaMailDomain — v-list-mail-domains JSON:
//
//	{ "<domain>": { "ANTIVIRUS": "yes"|"no", "ANTISPAM": "...",
//	                "ACCOUNTS": "5", "U_DISK": "...",
//	                "SUSPENDED": "no" } }
type hestiaMailDomainAttrs struct {
	Antivirus string `json:"ANTIVIRUS"`
	Antispam  string `json:"ANTISPAM"`
	Accounts  string `json:"ACCOUNTS"`
	UDisk     string `json:"U_DISK"`
	Suspended string `json:"SUSPENDED"`
}

// hestiaMailbox — v-list-mail-accounts JSON:
//
//	{ "<local>": { "U_DISK": "1.5", "QUOTA": "1024",
//	               "SUSPENDED": "no" } }
type hestiaMailboxAttrs struct {
	UDisk     string `json:"U_DISK"`
	Quota     string `json:"QUOTA"`
	Suspended string `json:"SUSPENDED"`
}

func (d *Discoverer) describeMailboxes(ctx context.Context, s *session, account string) ([]migrate.MailboxSpec, []migrate.Warning, error) {
	out, err := s.run(ctx, d.CommandTimeout,
		fmt.Sprintf("v-list-mail-domains '%s' json",
			strings.ReplaceAll(account, "'", `'\''`)))
	if err != nil {
		return nil, []migrate.Warning{{
			Code:   "hestiacp_mail_domains_failed",
			Detail: fmt.Sprintf("%v", err),
			At:     time.Now().UTC(),
		}}, nil
	}
	var mdoms map[string]hestiaMailDomainAttrs
	if err := json.Unmarshal(out, &mdoms); err != nil {
		return nil, nil, fmt.Errorf("v-list-mail-domains decode: %w", err)
	}
	rows := []migrate.MailboxSpec{}
	warnings := []migrate.Warning{}
	for dom := range mdoms {
		mout, merr := s.run(ctx, d.CommandTimeout,
			fmt.Sprintf("v-list-mail-accounts '%s' '%s' json",
				strings.ReplaceAll(account, "'", `'\''`),
				strings.ReplaceAll(dom, "'", `'\''`)))
		if merr != nil {
			warnings = append(warnings, migrate.Warning{
				Code:   "hestiacp_mail_accounts_failed",
				Detail: fmt.Sprintf("v-list-mail-accounts %s: %v", dom, merr),
				At:     time.Now().UTC(),
			})
			continue
		}
		var boxes map[string]hestiaMailboxAttrs
		if jErr := json.Unmarshal(mout, &boxes); jErr != nil {
			warnings = append(warnings, migrate.Warning{
				Code:   "hestiacp_mail_accounts_decode_failed",
				Detail: fmt.Sprintf("v-list-mail-accounts %s: %v", dom, jErr),
				At:     time.Now().UTC(),
			})
			continue
		}
		for local, b := range boxes {
			rows = append(rows, migrate.MailboxSpec{
				Address:    fmt.Sprintf("%s@%s", local, dom),
				BytesUsed:  parseHestiaMB(b.UDisk),
				QuotaBytes: parseHestiaQuota(b.Quota),
				MaildirPath: fmt.Sprintf(
					"/home/%s/mail/%s/%s",
					account, dom, local),
			})
		}
	}
	return rows, warnings, nil
}

// parseHestiaQuota: '1024' MB → bytes; '0' / ” / 'unlimited' → 0.
func parseHestiaQuota(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || strings.EqualFold(s, "unlimited") {
		return 0
	}
	return parseHestiaMB(s)
}

// splitCSV is the same shape as directadmin/describe.go's helper —
// kept package-private rather than extracted into a shared util so
// each importer's helper trio (loadSecret, run, splitCSV) stays
// tunable per package without abstraction debt.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
