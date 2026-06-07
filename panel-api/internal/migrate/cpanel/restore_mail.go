package cpanel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/agent"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/ids"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/models"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/repository"
)

// MailImportResult is returned to the restore-stage caller.
//
// Two fill modes per call site:
//
//   - When agentCli is nil → walks the tree locally and records
//     per-mailbox 'pending_manual' warnings (observation-only;
//     legacy stub behaviour preserved for callers that don't
//     want JMAP push).
//   - When agentCli is non-nil → dispatches migration.import_mailboxes
//     on the agent. Counts/bytes reflect what Stalwart actually
//     ingested via Email/import.
type MailImportResult struct {
	MaildirsFound  int
	MessagesFound  int
	MessagesPushed int64
	BytesFound     int64
	BytesPushed    int64
	Skipped        []string
}

// agentImportMailboxesResult mirrors panel-agent's
// migrationImportMailboxesResult shape so we can decode the JSON
// envelope coming back over the agent UDS.
type agentImportMailboxesResult struct {
	MailboxesProcessed int      `json:"mailboxes_processed"`
	MessagesImported   int64    `json:"messages_imported"`
	MessagesSkipped    int64    `json:"messages_skipped"`
	BytesImported      int64    `json:"bytes_imported"`
	Skipped            []string `json:"skipped,omitempty"`
}

// ImportMailboxes walks the homedir/mail/ tree of the parsed cpmove
// tarball + counts mailboxes/messages/bytes. When agentCli is non-
// nil, additionally dispatches migration.import_mailboxes on the
// agent which runs the JMAP Blob/upload + Email/import per message
// against Stalwart. Idempotent on resume: Stalwart Email/import
// dedupes on Message-ID, so re-running is a silent no-op for
// already-imported messages.
//
// jobID is forwarded to the agent so its path-prefix validation
// scopes the import to the right /var/lib/jabali-migrations/<id>/
// staging tree.
//
// agentCli nil → observation-only behaviour (legacy stub —
// per-mailbox 'pending_manual' warnings recorded; no JMAP push).
// Useful for tests + dry-run paths where Stalwart isn't reachable.
func ImportMailboxes(ctx context.Context, parsed *ParsedTarball, agentCli agent.AgentInterface, jobID string, mbRepo repository.MailboxRepository, domainsRepo repository.DomainRepository) (*MailImportResult, error) {
	if parsed == nil {
		return nil, fmt.Errorf("ImportMailboxes: parsed nil")
	}
	res := &MailImportResult{}
	if parsed.HomeDir == "" {
		// No homedir → no Maildir tree. Not fatal; record + return.
		res.Skipped = append(res.Skipped, "mailbox_skip:no_homedir_in_tarball")
		return res, nil
	}

	// MailRoot is set by per-importer parsers (cpanel: HomeDir/mail,
	// DA: HomeDir/email via the directadmin adapter). Fallback to
	// HomeDir/mail when caller didn't set it (legacy callers + cpanel
	// pre-MailRoot tarballs).
	mailRoot := parsed.MailRoot
	if mailRoot == "" {
		mailRoot = filepath.Join(parsed.HomeDir, "mail")
	}
	if !existsDir(mailRoot) {
		res.Skipped = append(res.Skipped, "mailbox_skip:no_mail_subtree")
		return res, nil
	}

	// cPanel "default"/system account: the owner mailbox sits in the
	// TOP-LEVEL Maildir (mail/cur, mail/new) rather than a per-domain
	// subdir. Count it here when OwnerEmail is known so messages_found
	// reflects it (the per-domain loop below skips top-level cur/new).
	// The agent imports the same mailbox off OwnerEmail; keeping the
	// count in step prevents the "0 found / N pushed" mismatch
	// (silent data loss masked by a clean manifest).
	if parsed.OwnerEmail != "" {
		if ownerMaildir, ok := looksLikeMaildir(mailRoot); ok {
			res.MaildirsFound++
			n, b := countMaildirMessages(ownerMaildir)
			res.MessagesFound += n
			res.BytesFound += b
		}
	}

	// cPanel Maildir layout: home/mail/<domain>/<localpart>/{cur,new,tmp}/
	// We walk to depth 2 (domain → localpart) and look for Maildir
	// markers (any of cur/, new/, tmp/) inside.
	domains, err := os.ReadDir(mailRoot)
	if err != nil {
		res.Skipped = append(res.Skipped, fmt.Sprintf("mail_read_root: %v", err))
		return res, nil
	}
	for _, dom := range domains {
		if !dom.IsDir() {
			continue
		}
		// Skip owner-Maildir slots/subfolders + cpanel metadata files
		// living alongside per-domain dirs. Same rule as
		// insertMailboxPanelRows below.
		name := dom.Name()
		if strings.HasPrefix(name, ".") || name == "cur" || name == "new" || name == "tmp" {
			continue
		}
		domPath := filepath.Join(mailRoot, name)
		users, err := os.ReadDir(domPath)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("mail_read_domain %s: %v", name, err))
			continue
		}
		for _, u := range users {
			if !u.IsDir() {
				continue
			}
			userPath := filepath.Join(domPath, u.Name())
			maildirPath, ok := looksLikeMaildir(userPath)
			if !ok {
				continue
			}
			res.MaildirsFound++
			n, b := countMaildirMessages(maildirPath)
			res.MessagesFound += n
			res.BytesFound += b
			// No per-mailbox 'pending_manual' line — the counter
			// totals (MessagesFound + later MessagesPushed) tell
			// the operator what landed; per-mailbox detail only
			// matters in the failure branch below, which appends
			// agent error context when push actually failed.
		}
	}
	// Observation-only fast path: no agent → return the per-
	// mailbox warning summary without dispatching JMAP.
	if agentCli == nil {
		if res.MaildirsFound > 0 {
			res.Skipped = append(res.Skipped,
				"mailboxes_pending_manual:agent unwired — observation-only mode")
		}
		return res, nil
	}

	// Agent path: hand the mail subtree to migration.import_mailboxes
	// which handles per-message Blob/upload + Email/import via JMAP.
	if jobID == "" {
		res.Skipped = append(res.Skipped, "mailbox_skip:no_job_id_for_agent_dispatch")
		return res, nil
	}
	raw, err := agentCli.Call(ctx, "migration.import_mailboxes", map[string]any{
		"job_id":       jobID,
		"src_mail_dir": mailRoot,
		"owner_email":  parsed.OwnerEmail, // empty → agent skips owner-mailbox detection
	})
	if err != nil {
		// Don't fail the whole restore stage on a mail import
		// failure — record + return so DBs/DNS/home are still
		// declared done and the operator can re-run mail import
		// in isolation later.
		res.Skipped = append(res.Skipped, fmt.Sprintf("agent.migration.import_mailboxes: %v", err))
		return res, nil
	}
	var ag agentImportMailboxesResult
	if err := json.Unmarshal(raw, &ag); err != nil {
		res.Skipped = append(res.Skipped, fmt.Sprintf("decode agent result: %v", err))
		return res, nil
	}
	res.MessagesPushed = ag.MessagesImported
	res.BytesPushed = ag.BytesImported
	if len(ag.Skipped) > 0 {
		res.Skipped = append(res.Skipped, ag.Skipped...)
	}

	// Insert panel mailboxes rows for every Maildir the agent imported.
	// Without these rows the UI and API are blind to the mailboxes even
	// though Stalwart holds the actual data.
	if mbRepo != nil && domainsRepo != nil {
		res.Skipped = append(res.Skipped, insertMailboxPanelRows(ctx, mailRoot, parsed.OwnerEmail, mbRepo, domainsRepo, res)...)
	}

	return res, nil
}

// insertMailboxPanelRows walks mailRoot/<domain>/<localpart> and creates
// a mailboxes panel row for each Maildir found. Returns extra skip/warn
// messages for the caller. Best-effort: a single failure does not abort
// other mailboxes.
func insertMailboxPanelRows(
	ctx context.Context,
	mailRoot string,
	ownerEmail string,
	mbRepo repository.MailboxRepository,
	domainsRepo repository.DomainRepository,
	res *MailImportResult,
) []string {
	var msgs []string

	// Owner/default account: top-level mail/{cur,new} → one panel
	// mailbox row keyed off OwnerEmail (<sourceUser>@<primaryDomain>).
	// Without this row the UI/API never show the owner mailbox even
	// though the agent pushed its messages into Stalwart.
	if ownerEmail != "" {
		if _, ok := looksLikeMaildir(mailRoot); ok {
			if at := strings.LastIndex(ownerEmail, "@"); at > 0 {
				lp, dn := ownerEmail[:at], ownerEmail[at+1:]
				msgs = append(msgs, insertOneMailboxRow(ctx, lp, dn, mbRepo, domainsRepo, res)...)
			}
		}
	}

	domains, err := os.ReadDir(mailRoot)
	if err != nil {
		return append(msgs, fmt.Sprintf("mailbox_rows: readdir mail root: %v", err))
	}
	for _, dom := range domains {
		if !dom.IsDir() {
			continue
		}
		name := dom.Name()
		// cPanel <homedir>/mail/ holds the OWNER's Maildir slots
		// alongside per-domain mailbox dirs:
		//   mail/example.com/<local>/Maildir/...    ← per-domain
		//   mail/.Drafts | .Junk | .Sent | .Trash | .spam
		//                                           ← owner Maildir subfolders
		//   mail/cur | new | tmp                    ← owner Maildir slots
		//   mail/.mailbox_format.cpanel             ← metadata file
		// Skip the owner-Maildir entries so the panel-row insert
		// doesn't 8x-warn on each restore.
		if strings.HasPrefix(name, ".") || name == "cur" || name == "new" || name == "tmp" {
			continue
		}
		usersDir := filepath.Join(mailRoot, dom.Name())
		users, uErr := os.ReadDir(usersDir)
		if uErr != nil {
			msgs = append(msgs, fmt.Sprintf("mailbox_rows: readdir %s: %v", usersDir, uErr))
			continue
		}
		for _, u := range users {
			if !u.IsDir() {
				continue
			}
			localPart := u.Name()
			if _, ok := looksLikeMaildir(filepath.Join(usersDir, localPart)); !ok {
				continue
			}
			msgs = append(msgs, insertOneMailboxRow(ctx, localPart, name, mbRepo, domainsRepo, res)...)
		}
	}
	return msgs
}

// insertOneMailboxRow creates a single panel mailboxes row for
// localPart@domainName. Shared by the per-domain loop and the owner/
// default-account path (top-level mail/{cur,new}). Best-effort: every
// outcome (domain-not-found, already-exists, create error, success) is
// returned as a skip/info line; the caller never aborts on one row.
func insertOneMailboxRow(
	ctx context.Context,
	localPart, domainName string,
	mbRepo repository.MailboxRepository,
	domainsRepo repository.DomainRepository,
	res *MailImportResult,
) []string {
	domain, dErr := domainsRepo.FindByName(ctx, domainName)
	if dErr != nil {
		return []string{fmt.Sprintf("mailbox_rows: domain %s not found in panel: %v", domainName, dErr)}
	}
	exists, exErr := mbRepo.ExistsByDomainAndLocalPart(ctx, domain.ID, localPart)
	if exErr != nil {
		return []string{fmt.Sprintf("mailbox_rows: exists check %s@%s: %v", localPart, domainName, exErr)}
	}
	if exists {
		return []string{fmt.Sprintf("mailbox_rows: %s@%s already exists", localPart, domainName)}
	}
	tempPwd := ids.NewULID()
	hash, hErr := bcrypt.GenerateFromPassword([]byte(tempPwd), bcrypt.DefaultCost)
	if hErr != nil {
		return []string{fmt.Sprintf("mailbox_rows: bcrypt %s@%s: %v", localPart, domainName, hErr)}
	}
	mb := &models.Mailbox{
		ID:           ids.NewULID(),
		DomainID:     domain.ID,
		LocalPart:    localPart,
		PasswordHash: string(hash),
		QuotaBytes:   1073741824,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if cErr := mbRepo.Create(ctx, mb); cErr != nil {
		return []string{fmt.Sprintf("mailbox_rows: create %s@%s: %v", localPart, domainName, cErr)}
	}
	res.MaildirsFound++
	return []string{fmt.Sprintf(
		"mailbox_rows: created %s@%s (temp_pwd=%s) — change via panel",
		localPart, domainName, tempPwd)}
}

// looksLikeMaildir checks for the Maildir-spec directory markers.
// Returns the actual Maildir path (which may be `path` itself for
// cpanel/Hestia layout, or `path/Maildir` for DA layout) and a
// bool indicating whether a Maildir-shaped tree was found.
func looksLikeMaildir(path string) (string, bool) {
	for _, marker := range []string{"cur", "new"} {
		if existsDir(filepath.Join(path, marker)) {
			return path, true
		}
	}
	dapath := filepath.Join(path, "Maildir")
	for _, marker := range []string{"cur", "new"} {
		if existsDir(filepath.Join(dapath, marker)) {
			return dapath, true
		}
	}
	return "", false
}

// countMaildirMessages walks cur/, new/, tmp/ inside a Maildir +
// sums file count + bytes. Skips directories silently — Maildir's
// spec says cur/new/tmp contain only regular files.
func countMaildirMessages(maildir string) (count int, bytes int64) {
	for _, sub := range []string{"cur", "new", "tmp"} {
		dir := filepath.Join(maildir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			// Basic Maildir filename sanity: should contain a
			// flag delimiter ':' OR be a unique name. We accept
			// any regular file rather than over-filter, since
			// some clients store quirky names.
			_ = strings.Contains(e.Name(), ":")
			count++
			bytes += info.Size()
		}
	}
	return count, bytes
}
