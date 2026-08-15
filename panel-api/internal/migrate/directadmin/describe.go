package directadmin

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// describe.go — per-area builders for the DA importer.
//
// **STATUS:** Coded against documented DirectAdmin CLI output
// shapes (DA admin docs + community wiki). Has NOT been validated
// against a live DA host; operator running this against an
// unfamiliar DA minor should expect potential field-set drift +
// surface failures via the manifest's Warnings list (every
// per-area call records partial/missing data without failing the
// whole describe). When a live DA fixture lands in the test corpus
// the builders get a real test pass.
//
// DA exposes account state via `da admin user.show <user>` (INI
// k=v lines), `da admin db.list <user>` (one db name per line),
// and `da admin pop.list <domain>` (one mailbox per line). Per-
// domain attrs come from `da admin user.show <user> -d <domain>`.
//
// Cron jobs + SSH authorized_keys are NOT exposed via the admin
// CLI on DA. Operator path: pull v-backup-user-style tarball,
// parse cron/<user> + .ssh/authorized_keys from the extracted
// tree (cpanel/restore_cron.go + cpanel/restore_sshkeys.go
// already do this via filesystem walk; DA tarballs follow a
// nearly-identical layout — Step 4 follow-up adds a thin DA
// tarball parser that reuses the cpanel writers).

// parseINI splits 'k=v\nk=v\n' lines into a map. Comments (#) and
// blank lines skipped. Trailing whitespace stripped. Used for
// every `da admin user.show` shaped output.
func parseINI(raw []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// describeDomains pulls the source account's domain list +
// per-domain detail. `da admin user.show <user>` returns a
// 'domains=' comma-separated list. Per-domain detail (docroot,
// PHP version) comes from a follow-up `user.show <user> -d
// <domain>` call which DA documents as the per-domain query.
func (d *Discoverer) describeDomains(ctx context.Context, s *session, account string) ([]migrate.DomainSpec, error) {
	// Read /usr/local/directadmin/data/users/<user>/{user.conf,
	// domains.list, domains/<dom>.conf}. DA's per-user state lives
	// in flat KEY=value files; no daemon round-trip needed. The
	// older code shelled out to phantom `da admin user.show <user>`
	// + `da admin user.show -d <dom>` which don't exist on real
	// DA installs (verified live, May 2026).
	userConf, err := s.run(ctx, d.CommandTimeout,
		fmt.Sprintf("cat /usr/local/directadmin/data/users/%s/user.conf 2>/dev/null", shellEscape(account)))
	if err != nil {
		return nil, fmt.Errorf("read DA user.conf %s: %w", account, err)
	}
	attrs := parseDAConf(userConf)
	primary := attrs["domain"]

	domsList, _ := s.run(ctx, d.CommandTimeout,
		fmt.Sprintf("cat /usr/local/directadmin/data/users/%s/domains.list 2>/dev/null", shellEscape(account)))
	allDomains := splitLines(domsList)
	if primary == "" && len(allDomains) > 0 {
		primary = allDomains[0]
	}

	rows := []migrate.DomainSpec{}
	for _, dom := range allDomains {
		spec := migrate.DomainSpec{
			Name:      dom,
			DocRoot:   fmt.Sprintf("/home/%s/domains/%s/public_html", account, dom),
			IsPrimary: dom == primary,
			HasPHP:    true,
		}
		dout, derr := s.run(ctx, d.CommandTimeout,
			fmt.Sprintf("cat /usr/local/directadmin/data/users/%s/domains/%s.conf 2>/dev/null",
				shellEscape(account), shellEscape(dom)))
		if derr == nil {
			dattrs := parseDAConf(dout)
			if v := dattrs["php1_select"]; v != "" {
				spec.PHPVer = v // DA newer field
			} else if v := dattrs["php_ver"]; v != "" {
				spec.PHPVer = v
			}
			if v := dattrs["public_html"]; v != "" {
				spec.DocRoot = v
			}
			if v := dattrs["php"]; v != "" {
				spec.HasPHP = strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
			}
		}
		rows = append(rows, spec)
	}
	return rows, nil
}

// parseDAConf parses DA's KEY=value file format. Whitespace-tolerant,
// blank/comment lines skipped. Same shape as parseINI but renamed
// to reflect the new file-read source.
func parseDAConf(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		out[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
	}
	return out
}

// splitLines returns non-blank/non-comment lines.
func splitLines(b []byte) []string {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// splitCSV splits a comma-separated value string with whitespace
// trimming + empty-token filtering. DA's domains= field is the
// canonical use.
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

// describeDatabases pulls the source's MariaDB DB list. DA's
// db.list output is one DB name per line, prefixed with the
// source-side username (e.g. 'cpuser_blogdb'). We strip the
// prefix later at restore time so the destination's username
// prefix can be applied (cpanel/restore_dbs.go does the same
// transform — DA reuses that writer once the tarball parser ships).
func (d *Discoverer) describeDatabases(ctx context.Context, s *session, account string) ([]migrate.DatabaseSpec, []migrate.Warning, error) {
	// Use DA's stashed admin creds at /usr/local/directadmin/conf/my.cnf
	// + SHOW DATABASES LIKE '<user>_%' to enumerate. Reliable across
	// DA minors; doesn't depend on phantom `da admin db.list` CLI.
	out, err := s.run(ctx, d.CommandTimeout,
		fmt.Sprintf("mysql --defaults-file=/usr/local/directadmin/conf/my.cnf -BN -e \"SHOW DATABASES\" 2>/dev/null | grep %s || true",
			shellQuoteForGrep("^"+account+"_")))
	if err != nil {
		return nil, []migrate.Warning{{
			Code:   "directadmin_db_list_failed",
			Detail: fmt.Sprintf("mysql SHOW DATABASES failed for %q: %v", account, err),
			At:     time.Now().UTC(),
		}}, nil
	}
	specs := []migrate.DatabaseSpec{}
	for _, name := range splitLines(out) {
		specs = append(specs, migrate.DatabaseSpec{Engine: "mysql", Name: name})
	}
	return specs, nil, nil
}

// shellQuoteForGrep produces a grep -E safe regex literal in single
// quotes. account is whitelisted (looksLikeDAUsername) so we don't
// need to escape regex metachars.
func shellQuoteForGrep(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// describeMailboxes walks every domain via 'da admin pop.list
// <domain>' to enumerate mailboxes. DA exposes per-mailbox quota
// via 'da admin pop.show <domain> -u <user>' which we don't pull
// here (per-mailbox round-trip cost vs the value of the projection
// — operator can manually inspect the source if quota math
// matters). Mailbox addresses returned as <local>@<domain>.
func (d *Discoverer) describeMailboxes(ctx context.Context, s *session, account string, domains []migrate.DomainSpec) ([]migrate.MailboxSpec, []migrate.Warning, error) {
	rows := []migrate.MailboxSpec{}
	warnings := []migrate.Warning{}
	for _, dom := range domains {
		// DA stores mailbox accounts at /etc/virtual/<dom>/passwd
		// (one local-part:hash:uid:gid line per mailbox). Cheaper
		// + format-stable across DA minors than the phantom
		// `da admin pop.list` CLI the old code shelled out to.
		out, err := s.run(ctx, d.CommandTimeout,
			fmt.Sprintf("cat /etc/virtual/%s/passwd 2>/dev/null", shellEscape(dom.Name)))
		if err != nil {
			warnings = append(warnings, migrate.Warning{
				Code:   "directadmin_virtual_passwd_failed",
				Detail: fmt.Sprintf("read /etc/virtual/%s/passwd: %v", dom.Name, err),
				At:     time.Now().UTC(),
			})
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			local := line
			if i := strings.Index(line, ":"); i > 0 {
				local = line[:i]
			}
			rows = append(rows, migrate.MailboxSpec{
				Address: fmt.Sprintf("%s@%s", local, dom.Name),
				MaildirPath: fmt.Sprintf(
					"/home/%s/imap/%s/%s/Maildir",
					account, dom.Name, local),
			})
		}
	}
	_ = account
	return rows, warnings, nil
}

// shellEscape replaces single quotes with the canonical `'\”`
// trick + nothing else. DA usernames + domain names already
// validated by the SSH-side principal probe; this is the
// belt-and-braces fallback for the remote-shell single-quote
// interpolation we use throughout the package.
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// parseQuotaMB converts DA's quota fields ('1024', '0', 'unlimited')
// to bytes. 'unlimited' / '0' → 0 (caller treats as 'no quota').
func parseQuotaMB(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "unlimited") {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v * 1024 * 1024
}
