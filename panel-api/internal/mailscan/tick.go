package mailscan

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// IngestHit is the payload the tick hands back to panel-api's malware
// ingest path. Panel-api wires this to the same code path as the agent
// post-scan hook (security_malware.go ingestEvent) so quarantine rows +
// M14 notifications fire identically across maldet and yara sources.
type IngestHit struct {
	Source         string
	EventType      string // "file_hit"
	Severity       string // "warn"
	Username       string // tenant email (we don't have a panel user mapping for Stalwart accounts)
	OriginalPath   string // pseudo-path: smtp:<from>/<attachment>
	QuarantinePath string // jmap://<accountId>/<emailId>
	Signature      string // YARA rule name
	SHA256         string // optional — caller may compute
	SizeBytes      int64
	RawJSON        map[string]any
}

// IngestFunc is the contract the tick uses to hand a hit to the rest of
// panel-api. Injected at construction so the tick package stays free of
// the api/ import (avoids cycle: api → mailscan → api).
type IngestFunc func(ctx context.Context, h IngestHit) error

// Settings is the subset of malware_settings the tick reads each cycle.
// Pulled fresh per tick so an admin toggle takes effect on the next
// cycle without daemon restart.
type Settings struct {
	Enabled            bool
	AllFolders         bool
	SkipAddresses      []string // lower-cased, trimmed
	MaxAttachmentBytes int64
	ScanTimeout        time.Duration
	PerTickBudget      int
	QuarantineFolder   string // default "Malware"
}

// Deps groups the tick's dependencies. Constructed once at app boot.
type Deps struct {
	State    repository.MailScanStateRepository
	Failures repository.MailScanFailureRepository
	Settings repository.MalwareSettingsRepository
	Ingest   IngestFunc
	Log      *slog.Logger
}

// lockPath is the file lock that ensures only one tick runs at a time
// across systemd timer races + reconciler ticker overlap. Lives under
// /run/jabali-panel/ (panel-api owns this dir; /run/jabali is root-only
// and panel-api runs as `jabali`).
const lockPath = "/run/jabali-panel/mail-scan.lock"

// StartTicker runs RunOnce on a 5-minute cadence until ctx is cancelled.
// Logs but does not propagate per-tick errors — the next tick retries.
// Off-by-default: skips work when settings.Enabled is false.
func StartTicker(ctx context.Context, deps Deps, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	deps.Log.Info("mailscan ticker starting", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Don't fire on startup — wait one interval so panel-api boot-storm
	// doesn't pile a YARA scan on top of every other reconciler.
	for {
		select {
		case <-ctx.Done():
			deps.Log.Info("mailscan ticker stopping")
			return
		case <-ticker.C:
			if err := RunOnce(ctx, deps); err != nil {
				deps.Log.Warn("mailscan tick failed", "err", err)
			}
		}
	}
}

// RunOnce executes a single tick. Returns nil when the tick was skipped
// (disabled / locked) or completed cleanly; non-nil only on systemic
// failure (settings DB read, lock acquire). Per-account/per-mailbox
// failures are absorbed into the DLQ and surfaced via UI.
func RunOnce(ctx context.Context, deps Deps) error {
	st, err := deps.Settings.Get(ctx)
	if err != nil {
		return fmt.Errorf("read malware_settings: %w", err)
	}
	cfg := settingsFromRow(st)
	if !cfg.Enabled {
		return nil
	}
	unlock, locked, err := tryLock(lockPath)
	if err != nil {
		return fmt.Errorf("acquire %s: %w", lockPath, err)
	}
	if !locked {
		deps.Log.Info("mailscan: previous tick still running — skipping this cycle")
		return nil
	}
	defer unlock()

	client, err := NewClient(ctx)
	if err != nil {
		return fmt.Errorf("jmap client: %w", err)
	}

	caller := client.CallerAccountID()
	if caller == "" {
		caller = "admin" // safe default — Stalwart accepts the username field
	}
	principals, err := client.ListPrincipals(ctx, caller)
	if err != nil {
		return fmt.Errorf("list principals: %w", err)
	}
	deps.Log.Info("mailscan tick", "principals", len(principals), "budget", cfg.PerTickBudget)

	budget := cfg.PerTickBudget
	for _, p := range principals {
		if budget <= 0 {
			break
		}
		if isSkipped(p.Email, cfg.SkipAddresses) {
			continue
		}
		used, err := scanAccount(ctx, client, deps, cfg, p, budget)
		budget -= used
		if err != nil {
			recordFailure(ctx, deps, p.ID, "", err.Error(), "scan_account")
			continue
		}
	}
	return nil
}

// scanAccount walks one tenant's mailboxes within the remaining budget.
// Returns the number of emails scanned (used against the per-tick
// budget so a chatty tenant doesn't starve others — the next tick picks
// up where this one left off via the scanned_at ASC ordering).
func scanAccount(ctx context.Context, client *Client, deps Deps, cfg Settings, p Principal, budget int) (int, error) {
	mailboxIDs := []string{}
	if cfg.AllFolders {
		ids, err := client.QueryAllScannableFolders(ctx, p.ID)
		if err != nil {
			return 0, fmt.Errorf("query mailboxes: %w", err)
		}
		mailboxIDs = ids
	} else {
		inbox, err := client.QueryInbox(ctx, p.ID)
		if err != nil {
			return 0, fmt.Errorf("query inbox: %w", err)
		}
		if inbox != "" {
			mailboxIDs = []string{inbox}
		}
	}

	used := 0
	for _, mbID := range mailboxIDs {
		if budget-used <= 0 {
			break
		}
		n, err := scanMailbox(ctx, client, deps, cfg, p, mbID, budget-used)
		used += n
		if err != nil {
			recordFailure(ctx, deps, p.ID, mbID, err.Error(), "scan_mailbox")
		}
	}
	return used, nil
}

// scanMailbox: read state cursor → query new emails since cursor → for
// each email, fetch attachments + scan; on hit move + emit ingest;
// always advance cursor at the end so even clean-scan mailboxes don't
// re-scan next tick.
func scanMailbox(ctx context.Context, client *Client, deps Deps, cfg Settings, p Principal, mailboxID string, budget int) (int, error) {
	state, err := deps.State.Get(ctx, p.ID, mailboxID)
	if err != nil && err != repository.ErrNotFound {
		return 0, fmt.Errorf("read state: %w", err)
	}
	var since *time.Time
	if state != nil {
		since = state.LastReceivedAt
	} else {
		state = &models.MailScanState{AccountID: p.ID, MailboxID: mailboxID}
	}

	limit := budget
	if limit > 100 {
		limit = 100 // JMAP per-call ceiling; we'll come back next tick
	}
	emailIDs, err := client.QueryNewEmails(ctx, p.ID, mailboxID, since, limit)
	if err != nil {
		return 0, fmt.Errorf("Email/query: %w", err)
	}
	if len(emailIDs) == 0 {
		// Nothing new: do NOT write. This branch used to bump ScannedAt and
		// Upsert on every idle mailbox, every 5 minutes, forever — one DB
		// write per mailbox per tick purely to record "still nothing".
		//
		// Safe to skip because the scan cursor is LastReceivedAt, not
		// ScannedAt: `since` above reads LastReceivedAt, and nothing else in
		// the codebase consumes ScannedAt. A brand-new mailbox with no state
		// row also stays unwritten until it actually has mail to scan, which
		// is when the row starts carrying information.
		return 0, nil
	}

	emails, err := client.GetEmailsWithAttachments(ctx, p.ID, emailIDs)
	if err != nil {
		return 0, fmt.Errorf("Email/get: %w", err)
	}

	for _, e := range emails {
		state.ScannedCount++
		state.LastEmailID = strPtr(e.ID)
		ts := e.ReceivedAt
		if !ts.IsZero() {
			state.LastReceivedAt = &ts
		}
		// Skip messages with no attachments — JMAP `hasAttachment` is
		// authoritative; saves a Email/get bodyValues round-trip.
		if !e.HasAttachment || len(e.Attachments) == 0 {
			continue
		}
		for _, att := range e.Attachments {
			if att.Size > cfg.MaxAttachmentBytes {
				recordFailure(ctx, deps, p.ID, mailboxID, fmt.Sprintf("attachment %q exceeds max %d bytes", att.Name, cfg.MaxAttachmentBytes), "attachment_too_large")
				continue
			}
			if err := scanOneAttachment(ctx, client, deps, cfg, p, mailboxID, e, att, state); err != nil {
				recordFailure(ctx, deps, p.ID, mailboxID, err.Error(), "scan_attachment")
			}
		}
	}
	state.ScannedAt = time.Now().UTC()
	_ = deps.State.Upsert(ctx, state)
	return len(emails), nil
}

// scanOneAttachment downloads + scans + (on hit) quarantines + ingests
// a single attachment. Returns nil on clean scan or successful
// quarantine; non-nil only on infrastructure failures (download,
// JMAP move). YARA hits are NOT errors — they're the happy path.
func scanOneAttachment(ctx context.Context, client *Client, deps Deps, cfg Settings, p Principal, mailboxID string, e EmailMetadata, att EmailPart, state *models.MailScanState) error {
	dlCtx, cancel := timeoutCtx(ctx, cfg.ScanTimeout*2) // download budget = 2x scan
	defer cancel()
	body, truncated, err := client.DownloadAttachment(dlCtx, p.ID, att, cfg.MaxAttachmentBytes)
	if err != nil {
		return fmt.Errorf("download %q: %w", att.Name, err)
	}
	if deps.Log != nil {
		deps.Log.Info("mailscan attachment downloaded",
			"account", p.ID, "email", e.ID, "attachment", att.Name,
			"jmap_size", att.Size, "downloaded_bytes", len(body), "truncated", truncated)
	}
	if truncated {
		recordFailure(ctx, deps, p.ID, mailboxID, fmt.Sprintf("attachment %q truncated at %d bytes", att.Name, cfg.MaxAttachmentBytes), "attachment_truncated")
		return nil
	}

	scanCtx, scanCancel := timeoutCtx(ctx, cfg.ScanTimeout)
	res := scanBytes(scanCtx, body, att.Name)
	scanCancel()
	if deps.Log != nil {
		deps.Log.Info("mailscan attachment scanned",
			"account", p.ID, "email", e.ID, "attachment", att.Name,
			"rule", res.RuleName, "engine_err", res.EngineErr)
	}
	if res.EngineErr != nil {
		return fmt.Errorf("scan %q: %w", att.Name, res.EngineErr)
	}
	if res.RuleName == "" {
		return nil // clean
	}

	// Hit. Resolve quarantine mailbox (lazy create + 24h revalidate).
	qid, err := resolveQuarantineMailbox(ctx, client, deps, p.ID, state, cfg.QuarantineFolder)
	if err != nil {
		return fmt.Errorf("ensure quarantine mailbox: %w", err)
	}
	if err := client.MoveEmailToMailbox(ctx, p.ID, e.ID, qid); err != nil {
		return fmt.Errorf("move email %s: %w", e.ID, err)
	}
	state.HitCount++

	// Hand off to panel-api ingest.
	from := ""
	if len(e.From) > 0 {
		from = e.From[0].Email
	}
	subject := e.Subject
	if len(subject) > 80 {
		subject = subject[:80] + "…"
	}
	hit := IngestHit{
		Source:         "yara",
		EventType:      "file_hit",
		Severity:       "warn",
		Username:       p.Email,
		OriginalPath:   fmt.Sprintf("smtp:%s/%s", from, att.Name),
		QuarantinePath: fmt.Sprintf("jmap://%s/%s", p.ID, e.ID),
		Signature:      res.RuleName,
		SizeBytes:      att.Size,
		RawJSON: map[string]any{
			"mail_id":      e.ID,
			"account_id":   p.ID,
			"mailbox_id":   mailboxID,
			"quarantine_mailbox_id": qid,
			"from":         from,
			"subject":      subject,
			"attachment":   att.Name,
			"mime_type":    att.Type,
			"size_bytes":   att.Size,
			"yara_rule":    res.RuleName,
			"received_at":  e.ReceivedAt.Format(time.RFC3339),
		},
	}
	if err := deps.Ingest(ctx, hit); err != nil {
		// Move succeeded; ingest failed. Surface in DLQ so we don't
		// silently lose the M14 notification. The mail is already
		// quarantined so the operator is protected — the failure is
		// observability-only.
		recordFailure(ctx, deps, p.ID, mailboxID, fmt.Sprintf("ingest hit for %s: %v", e.ID, err), "ingest_failed")
	}
	return nil
}

// resolveQuarantineMailbox returns the cached quarantine mailbox id,
// validating it once every 24h via Mailbox/get notFound. Lazy-creates
// when the cache is empty or invalidated.
func resolveQuarantineMailbox(ctx context.Context, client *Client, deps Deps, accountID string, state *models.MailScanState, name string) (string, error) {
	const revalidateAfter = 24 * time.Hour
	if state.QuarantineMailbox != nil && state.QuarantineMailboxVerified != nil &&
		time.Since(*state.QuarantineMailboxVerified) < revalidateAfter {
		return *state.QuarantineMailbox, nil
	}
	if state.QuarantineMailbox != nil {
		ok, err := client.MailboxExists(ctx, accountID, *state.QuarantineMailbox)
		if err == nil && ok {
			now := time.Now().UTC()
			state.QuarantineMailboxVerified = &now
			return *state.QuarantineMailbox, nil
		}
	}
	id, err := client.EnsureQuarantineMailbox(ctx, accountID, name)
	if err != nil {
		return "", err
	}
	state.QuarantineMailbox = &id
	now := time.Now().UTC()
	state.QuarantineMailboxVerified = &now
	return id, nil
}

// recordFailure inserts a DLQ row best-effort. Errors against the DLQ
// itself are logged but never propagated — losing one failure record is
// preferable to looping on a broken table.
func recordFailure(ctx context.Context, deps Deps, accountID, mailboxID, detail, reason string) {
	if deps.Failures == nil {
		return
	}
	rec := &models.MailScanFailure{
		ID:         ids.NewULID(),
		AccountID:  accountID,
		MailboxID:  mailboxID,
		Reason:     reason,
		Detail:     strPtr(detail),
	}
	if err := deps.Failures.Create(ctx, rec); err != nil && deps.Log != nil {
		deps.Log.Warn("mailscan: DLQ insert failed", "err", err, "reason", reason)
	}
}

func settingsFromRow(s *models.MalwareSettings) Settings {
	out := Settings{
		Enabled:            s.MailScanEnabled,
		AllFolders:         s.MailScanAllFolders,
		MaxAttachmentBytes: int64(s.MailScanMaxAttachmentMB) * 1024 * 1024,
		ScanTimeout:        time.Duration(s.MailScanTimeoutSec) * time.Second,
		PerTickBudget:      int(s.MailScanPerTickBudget),
		QuarantineFolder:   "Malware",
	}
	if s.MailScanSkipAddresses != "" {
		for _, a := range strings.Split(s.MailScanSkipAddresses, ",") {
			a = strings.ToLower(strings.TrimSpace(a))
			if a != "" {
				out.SkipAddresses = append(out.SkipAddresses, a)
			}
		}
	}
	if out.MaxAttachmentBytes <= 0 {
		out.MaxAttachmentBytes = 25 * 1024 * 1024
	}
	if out.ScanTimeout <= 0 {
		out.ScanTimeout = 10 * time.Second
	}
	if out.PerTickBudget <= 0 {
		out.PerTickBudget = 200
	}
	return out
}

func isSkipped(email string, list []string) bool {
	if email == "" {
		return false
	}
	e := strings.ToLower(email)
	for _, s := range list {
		if s == e {
			return true
		}
	}
	return false
}

func strPtr(s string) *string { return &s }

// ---- file lock ----

var lockOnce sync.Once

// tryLock attempts an exclusive non-blocking flock on `path`. Returns
// (unlock, true, nil) on success; (nil, false, nil) when another
// process holds the lock; (nil, false, err) on real failure.
func tryLock(path string) (func(), bool, error) {
	lockOnce.Do(func() { _ = os.MkdirAll("/run/jabali-panel", 0o750) })
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
