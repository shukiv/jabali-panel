package plesk

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// mail.go — mailbox detection for the Plesk importer.
//
// Plesk stores mailboxes at PLESK_MAILNAMES_D (default /var/qmail/
// mailnames), one directory per mail name: <base>/<domain>/<local>/
// Maildir. We enumerate the filesystem per owned domain rather than
// parsing `plesk bin mail -l -json` — the filesystem is authoritative,
// yields the exact Maildir path the (later) import step needs, and
// avoids a version-varying JSON shape. Quota isn't exposed on disk, so
// QuotaBytes is left 0 (unknown/unlimited).

const pleskMailnamesRoot = "/var/qmail/mailnames"

// describeMailboxes lists the mailboxes under each of the account's
// domains, with a best-effort du size per box. Per-domain failures warn
// and continue rather than aborting the describe.
func (d *Discoverer) describeMailboxes(ctx context.Context, s *session, domains []migrate.DomainSpec) ([]migrate.MailboxSpec, []migrate.Warning) {
	warns := []migrate.Warning{}
	boxes := []migrate.MailboxSpec{}
	for _, dom := range domains {
		base := fmt.Sprintf("%s/%s", pleskMailnamesRoot, dom.Name)
		out, err := s.run(ctx, d.CommandTimeout, "ls -1 "+shellQuote(base)+" 2>/dev/null")
		if err != nil {
			warns = append(warns, migrate.Warning{
				Code:   "mailboxes_domain_failed",
				Detail: fmt.Sprintf("%s: %v", dom.Name, err),
			})
			continue
		}
		for _, local := range splitLines(string(out)) {
			if strings.HasPrefix(local, ".") || strings.Contains(local, "@") {
				continue // skip dotfiles / stray entries
			}
			maildir := fmt.Sprintf("%s/%s/Maildir", base, local)
			spec := migrate.MailboxSpec{
				Address:     local + "@" + dom.Name,
				MaildirPath: maildir,
			}
			if szOut, e := s.run(ctx, d.CommandTimeout, "du -sb "+shellQuote(maildir)+" 2>/dev/null | cut -f1"); e == nil {
				spec.BytesUsed = parseFirstInt(string(szOut))
			}
			boxes = append(boxes, spec)
		}
	}
	return boxes, warns
}

// parseFirstInt returns the first whitespace-separated integer field of
// s, or 0 when there isn't one. Robust whether or not a shell `cut`
// already reduced the input to a bare number.
func parseFirstInt(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return n
}
