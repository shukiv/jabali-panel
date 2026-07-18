package cpanel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
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
	MaildirsFound    int // maildir-shaped dirs the pre-scan found (message import + dispatch gate)
	MailboxesCreated int // GH #327: DISTINCT panel mailbox rows created — the operator-facing count
	MessagesFound    int
	MessagesPushed   int64
	BytesFound       int64
	BytesPushed      int64
	Skipped          []string
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
func ImportMailboxes(ctx context.Context, parsed *ParsedTarball, agentCli agent.AgentInterface, jobID string, mbRepo repository.MailboxRepository, domainsRepo repository.DomainRepository, preserveCreds bool) (*MailImportResult, error) {
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
		res.Skipped = append(res.Skipped, insertMailboxPanelRows(ctx, mailRoot, parsed.OwnerEmail, preserveCreds, mbRepo, domainsRepo, res)...)
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
	preserveCreds bool,
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
				srcHash := loadSourceMailPasswords(filepath.Join(mailRoot, dn))[lp]
				msgs = append(msgs, insertOneMailboxRow(ctx, lp, dn, srcHash, preserveCreds, mbRepo, domainsRepo, res)...)
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
		// HestiaCP ships the source mailbox password hashes in
		// mail/<domain>/conf/passwd (dovecot passwd-file). Load them once per
		// domain; insertOneMailboxRow uses the hash when --preserve-source-state
		// is set and it's a Stalwart-verifiable bcrypt.
		srcPwds := loadSourceMailPasswords(usersDir)
		users, uErr := os.ReadDir(usersDir)
		if uErr != nil {
			msgs = append(msgs, fmt.Sprintf("mailbox_rows: readdir %s: %v", usersDir, uErr))
			continue
		}
		seen := map[string]bool{}
		for _, u := range users {
			if !u.IsDir() {
				continue
			}
			localPart := u.Name()
			if _, ok := looksLikeMaildir(filepath.Join(usersDir, localPart)); !ok {
				continue
			}
			seen[localPart] = true
			msgs = append(msgs, insertOneMailboxRow(ctx, localPart, name, srcPwds[localPart], preserveCreds, mbRepo, domainsRepo, res)...)
		}
		// GH #327: HestiaCP creates a mail account with `mkdir mail/<dom>/<acct>`
		// but dovecot only materializes cur/new/tmp on first delivery/login — so a
		// freshly-created account is a BARE dir that looksLikeMaildir rejects, and
		// it never imported (reporter saw "1 of 3": only the account that had
		// received mail carried over). conf/passwd is the authoritative account
		// list — create a row for every account the Maildir walk missed. An empty
		// inbox is fine: Stalwart provisions its own store on first login.
		for _, localPart := range listSourceMailAccounts(usersDir) {
			if seen[localPart] {
				continue
			}
			seen[localPart] = true
			msgs = append(msgs, insertOneMailboxRow(ctx, localPart, name, srcPwds[localPart], preserveCreds, mbRepo, domainsRepo, res)...)
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
	srcHash string,
	preserveCreds bool,
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
	// JAB-88: preserve the source mailbox password when the operator opted in
	// (--preserve-source-state) AND the source carries a Stalwart-verifiable
	// bcrypt hash (Hestia stores {BLF-CRYPT}$2y$… — Stalwart's SQL directory
	// authenticates $2a/$2b/$2y bcrypt, live-verified). Otherwise set a fresh
	// random password and warn LOUDLY: the tenant is locked out until reset, and
	// silent lockout was the reported bug.
	passwordHash := srcHash
	preserved := preserveCreds && srcHash != ""
	if !preserved {
		tempPwd := ids.NewULID()
		hash, hErr := bcrypt.GenerateFromPassword([]byte(tempPwd), bcrypt.DefaultCost)
		if hErr != nil {
			return []string{fmt.Sprintf("mailbox_rows: bcrypt %s@%s: %v", localPart, domainName, hErr)}
		}
		passwordHash = string(hash)
	}
	mb := &models.Mailbox{
		ID:           ids.NewULID(),
		DomainID:     domain.ID,
		LocalPart:    localPart,
		PasswordHash: passwordHash,
		QuotaBytes:   1073741824,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if cErr := mbRepo.Create(ctx, mb); cErr != nil {
		return []string{fmt.Sprintf("mailbox_rows: create %s@%s: %v", localPart, domainName, cErr)}
	}
	// GH #327: the pre-scan already counted maildir-shaped dirs into
	// MaildirsFound; counting again here double-reported real mailboxes
	// (johnnyq saw "7" for 3). Count DISTINCT rows created separately.
	res.MailboxesCreated++
	if preserved {
		return []string{fmt.Sprintf(
			"mailbox_rows: created %s@%s — SOURCE password preserved (--preserve-source-state); the tenant's original mail password works",
			localPart, domainName)}
	}
	// Loud, explicit notice — a plain "temp password set" read as harmless and
	// the operator handed a locked-out mailbox to the tenant.
	return []string{fmt.Sprintf(
		"mailbox_rows: created %s@%s with a NEW password — the ORIGINAL mail password was NOT migrated; the mailbox owner is locked out until you reset it in the panel (use --preserve-source-state on a trusted same-owner migration to carry the source password)",
		localPart, domainName)}
}

// loadSourceMailPasswords reads a HestiaCP dovecot passwd-file at
// <domainDir>/conf/passwd and returns local_part → bcrypt hash (with any
// {SCHEME} tag stripped). Only bcrypt variants Stalwart's SQL directory can
// verify ($2a/$2b/$2y) are returned; SHA-CRYPT / plaintext / unknown schemes are
// dropped so the caller falls back to a fresh password + the lockout notice.
// Missing or unreadable file → empty map (cPanel, which has no such file, and
// any source without per-mailbox hashes simply get fresh passwords). Line
// format: <local>:{BLF-CRYPT}$2y$05$…:<uid>:<gid>:…
func loadSourceMailPasswords(domainDir string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(filepath.Join(domainDir, "conf", "passwd"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 2 || parts[0] == "" {
			continue
		}
		if h := normalizeSourceMailHash(parts[1]); h != "" {
			out[parts[0]] = h
		}
	}
	return out
}

// listSourceMailAccounts returns EVERY local_part in a HestiaCP dovecot
// passwd-file at <domainDir>/conf/passwd, regardless of hash scheme. Unlike
// loadSourceMailPasswords (which drops non-bcrypt hashes it can't preserve),
// this is the authoritative ACCOUNT LIST: a mailbox row must exist for every
// source account even when its Maildir hasn't materialized yet (GH #327) or its
// password can't be carried. Missing/unreadable file → nil (cPanel has no such
// file and keeps using the Maildir walk). Line format: <local>:<hash>:<uid>:…
func listSourceMailAccounts(domainDir string) []string {
	f, err := os.Open(filepath.Join(domainDir, "conf", "passwd"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		local := line
		if i := strings.IndexByte(line, ':'); i > 0 {
			local = line[:i]
		}
		if local == "" || strings.ContainsAny(local, ":") {
			continue
		}
		out = append(out, local)
	}
	return out
}

// stalwartHashPrefixes are the crypt-string prefixes Stalwart verifies for a
// stored account password (per stalw.art/docs/auth/authentication/password):
// bcrypt, SHA-512/256/MD5-crypt, Argon2, PBKDF2, scrypt, SHA-1-crypt. Source
// panels (cPanel/HestiaCP dovecot) export these as {SCHEME}$crypt strings; once
// the dovecot {SCHEME} tag is stripped, the bare $-prefixed crypt is exactly
// what Stalwart recognises by prefix.
var stalwartHashPrefixes = []string{
	"$2a$", "$2b$", "$2y$", // bcrypt
	"$6$",      // SHA-512 crypt
	"$5$",      // SHA-256 crypt
	"$1$",      // MD5 crypt
	"$argon2",  // $argon2id / $argon2i / $argon2d
	"$pbkdf2",  // PBKDF2 variants
	"$scrypt",  // scrypt
	"$sha1",    // SHA-1 crypt
}

// normalizeSourceMailHash strips a leading dovecot {SCHEME} tag and returns the
// bare crypt string if Stalwart can verify it (JAB-149). Previously this kept
// bcrypt only and dropped SHA-512/Argon2/etc., forcing a password reset on every
// non-bcrypt mailbox even though Stalwart verifies those schemes natively —
// which needlessly locked migrated users out of their existing password. A hash
// with no Stalwart-recognised prefix (e.g. {PLAIN} plaintext, unknown scheme) is
// still dropped: storing it would never authenticate.
func normalizeSourceMailHash(field string) string {
	h := field
	if strings.HasPrefix(h, "{") {
		if i := strings.IndexByte(h, '}'); i >= 0 {
			h = h[i+1:]
		}
	}
	for _, pfx := range stalwartHashPrefixes {
		if strings.HasPrefix(h, pfx) {
			return h
		}
	}
	return ""
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
