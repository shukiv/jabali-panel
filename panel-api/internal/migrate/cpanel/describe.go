package cpanel

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// uapiCmd renders a UAPI command line. When the connected principal
// is admin we prepend `--user=<account>` so the call lands in the
// right cPanel context; for a single-user principal the connected
// session already has --user implicit.
func (s *session) uapiCmd(account, module, fn string, kv ...string) string {
	parts := []string{"uapi", "--output=jsonpretty"}
	if s.principal == PrincipalAdmin {
		parts = append(parts, "--user="+account)
	}
	parts = append(parts, module, fn)
	parts = append(parts, kv...)
	return joinArgs(parts)
}

// joinArgs is shell-safe argv joining for the SSH exec line.
// Refuses any token containing a single quote — UAPI args we send
// are all alphanumeric + dot + slash + equals, so a quote indicates
// an injection attempt and we surface it as a panic during dev (
// caller bug — no operator-controlled string ever reaches here).
func joinArgs(parts []string) string {
	out := ""
	for i, p := range parts {
		for _, r := range p {
			if r == '\'' {
				panic("cpanel.joinArgs: argv token contains single quote")
			}
		}
		if i > 0 {
			out += " "
		}
		out += "'" + p + "'"
	}
	return out
}

// describeDomains pulls the cPanel docroot map. UAPI
// DomainInfo::list_domains returns four arrays: main_domain,
// addon_domains[], parked_domains[], sub_domains[]. We collapse
// them into DomainSpec rows; primary flag set on main_domain only.
type domainsPayload struct {
	MainDomain    string   `json:"main_domain"`
	AddonDomains  []string `json:"addon_domains"`
	ParkedDomains []string `json:"parked_domains"`
	SubDomains    []string `json:"sub_domains"`
}

func (d *Discoverer) describeDomains(ctx context.Context, s *session, account string) ([]migrate.DomainSpec, error) {
	out, err := s.run(ctx, d.CommandTimeout, s.uapiCmd(account, "DomainInfo", "list_domains"))
	if err != nil {
		return nil, fmt.Errorf("DomainInfo list_domains: %w", err)
	}
	var p domainsPayload
	if err := decodeUAPI(out, &p); err != nil {
		return nil, fmt.Errorf("DomainInfo decode: %w", err)
	}
	rows := []migrate.DomainSpec{}
	if p.MainDomain != "" {
		rows = append(rows, migrate.DomainSpec{
			Name:      p.MainDomain,
			DocRoot:   fmt.Sprintf("/home/%s/public_html", account),
			IsPrimary: true,
			HasPHP:    true, // cPanel default; refined later via PHP version probe
		})
	}
	for _, d := range p.AddonDomains {
		rows = append(rows, migrate.DomainSpec{
			Name:    d,
			DocRoot: fmt.Sprintf("/home/%s/public_html/%s", account, d),
			HasPHP:  true,
		})
	}
	for _, d := range p.SubDomains {
		rows = append(rows, migrate.DomainSpec{
			Name:    d,
			DocRoot: fmt.Sprintf("/home/%s/public_html/%s", account, d),
			HasPHP:  true,
		})
	}
	for _, d := range p.ParkedDomains {
		// Parked domains share the main docroot; no separate row
		// per parked alias since jabali2's domain model already
		// folds parked aliases into the main domain.
		rows = append(rows, migrate.DomainSpec{
			Name:    d,
			DocRoot: fmt.Sprintf("/home/%s/public_html", account),
			HasPHP:  true,
		})
	}
	return rows, nil
}

// describeMailboxes pulls the POP/IMAP account list. UAPI
// Email::list_pops returns rows with `email`, `_diskused`,
// `diskquota`, `mtime`. We don't pull the Maildir path here —
// restore stage parses it from the tarball directly.
type mailboxRow struct {
	Email     string `json:"email"`
	DiskUsed  string `json:"_diskused"`
	DiskQuota string `json:"diskquota"`
}

func (d *Discoverer) describeMailboxes(ctx context.Context, s *session, account string) ([]migrate.MailboxSpec, error) {
	out, err := s.run(ctx, d.CommandTimeout, s.uapiCmd(account, "Email", "list_pops"))
	if err != nil {
		return nil, fmt.Errorf("Email list_pops: %w", err)
	}
	var rows []mailboxRow
	if err := decodeUAPI(out, &rows); err != nil {
		return nil, fmt.Errorf("Email list_pops decode: %w", err)
	}
	out2 := make([]migrate.MailboxSpec, 0, len(rows))
	for _, r := range rows {
		out2 = append(out2, migrate.MailboxSpec{
			Address:    r.Email,
			BytesUsed:  parseHumanBytes(r.DiskUsed),
			QuotaBytes: parseHumanBytes(r.DiskQuota),
		})
	}
	return out2, nil
}

// describeDatabases pulls MySQL DBs only. cPanel may expose
// PostgreSQL via a separate Postgresql module; we record those as
// warnings in the manifest (postgres skipped per ADR-0094).
type dbRow struct {
	Database  string `json:"database"`
	DiskUsage string `json:"disk_usage"`
}

func (d *Discoverer) describeDatabases(ctx context.Context, s *session, account string) ([]migrate.DatabaseSpec, []migrate.Warning, error) {
	out, err := s.run(ctx, d.CommandTimeout, s.uapiCmd(account, "Mysql", "list_databases"))
	if err != nil {
		return nil, nil, fmt.Errorf("Mysql list_databases: %w", err)
	}
	var rows []dbRow
	if err := decodeUAPI(out, &rows); err != nil {
		return nil, nil, fmt.Errorf("Mysql list_databases decode: %w", err)
	}
	specs := make([]migrate.DatabaseSpec, 0, len(rows))
	for _, r := range rows {
		specs = append(specs, migrate.DatabaseSpec{
			Engine: "mysql",
			Name:   r.Database,
			Bytes:  parseHumanBytes(r.DiskUsage),
		})
	}
	// Probe Postgresql module — failure is OK (most cPanel hosts
	// don't have PG installed). Success → emit warnings, no
	// DatabaseSpec rows; M37 importer integration handles PG later.
	warnings := []migrate.Warning{}
	pgOut, err := s.run(ctx, d.CommandTimeout, s.uapiCmd(account, "Postgresql", "list_databases"))
	if err == nil {
		var pgRows []dbRow
		if err := decodeUAPI(pgOut, &pgRows); err == nil && len(pgRows) > 0 {
			for _, r := range pgRows {
				warnings = append(warnings, migrate.Warning{
					Code:   "postgres_unsupported",
					Detail: fmt.Sprintf("PostgreSQL DB %q skipped — re-import after M37 integration ships", r.Database),
					At:     time.Now().UTC(),
				})
			}
		}
	}
	return specs, warnings, nil
}

// describeCron pulls cPanel's per-account crontab. UAPI Cron::list_cron
// returns rows shaped {minute, hour, day, month, weekday, command}.
type cronRow struct {
	Minute  string `json:"minute"`
	Hour    string `json:"hour"`
	Day     string `json:"day"`
	Month   string `json:"month"`
	Weekday string `json:"weekday"`
	Command string `json:"command"`
}

func (d *Discoverer) describeCron(ctx context.Context, s *session, account string) ([]migrate.CronSpec, error) {
	out, err := s.run(ctx, d.CommandTimeout, s.uapiCmd(account, "Cron", "list_cron"))
	if err != nil {
		return nil, fmt.Errorf("Cron list_cron: %w", err)
	}
	var rows []cronRow
	if err := decodeUAPI(out, &rows); err != nil {
		return nil, fmt.Errorf("Cron list_cron decode: %w", err)
	}
	specs := make([]migrate.CronSpec, 0, len(rows))
	for i, r := range rows {
		schedule := fmt.Sprintf("%s %s %s %s %s", r.Minute, r.Hour, r.Day, r.Month, r.Weekday)
		specs = append(specs, migrate.CronSpec{
			Name:     "cpanel-cron-" + strconv.Itoa(i+1),
			Schedule: schedule,
			Command:  r.Command,
			RunAs:    account,
		})
	}
	return specs, nil
}

// describeSSHKeys pulls authorized_keys via UAPI SSH::listkeys.
type sshRow struct {
	Name        string `json:"name"`
	Authorized  int    `json:"authorized"`
	HasShadow   int    `json:"has_shadow"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}

func (d *Discoverer) describeSSHKeys(ctx context.Context, s *session, account string) ([]migrate.SSHKeySpec, error) {
	out, err := s.run(ctx, d.CommandTimeout, s.uapiCmd(account, "SSH", "listkeys"))
	if err != nil {
		return nil, fmt.Errorf("SSH listkeys: %w", err)
	}
	var rows []sshRow
	if err := decodeUAPI(out, &rows); err != nil {
		return nil, fmt.Errorf("SSH listkeys decode: %w", err)
	}
	specs := make([]migrate.SSHKeySpec, 0, len(rows))
	for _, r := range rows {
		if r.Authorized != 1 || r.PublicKey == "" {
			continue
		}
		specs = append(specs, migrate.SSHKeySpec{
			Comment:     r.Name,
			PublicKey:   r.PublicKey,
			Fingerprint: r.Fingerprint,
		})
	}
	return specs, nil
}
