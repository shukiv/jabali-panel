package imap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// ImportConfig drives a single IMAP account migration end to end.
type ImportConfig struct {
	// Account is the remote IMAP login (host/port/user/app-password).
	Account ProbeConfig
	// TargetEmail is the local jabali mailbox the mail lands in. It must
	// already exist as a panel mailbox; the agent ensures the matching
	// Stalwart account. Passed as owner_email so the whole staged Maildir
	// (INBOX + Maildir++ subfolders) imports under this address.
	TargetEmail string
	// JobID scopes the staging dir + the agent's path-prefix validation.
	JobID string
	// StagingBase is the migrations working root the staging Maildir is
	// built under. It MUST be one of the agent's accepted staging roots
	// (/var/lib/jabali/migrations by default, or the legacy
	// /var/lib/jabali-migrations) or the agent rejects the import. Callers
	// pass migrate.MigrationsRoot(...).
	StagingBase string
	// Folders optionally restricts/pre-supplies the folder set (e.g. from
	// an earlier Probe the operator reviewed). nil → fetch lists them.
	Folders []Folder
}

// ImportResult is the end-to-end outcome for one account.
type ImportResult struct {
	Fetch            *FetchResult `json:"fetch"`
	MessagesImported int64        `json:"messages_imported"`
	BytesImported    int64        `json:"bytes_imported"`
	Skipped          []string     `json:"skipped,omitempty"`
}

// agentImportMailboxesResult mirrors panel-agent's
// migrationImportMailboxesResult so we can decode the JSON envelope.
type agentImportMailboxesResult struct {
	MailboxesProcessed int      `json:"mailboxes_processed"`
	MessagesImported   int64    `json:"messages_imported"`
	MessagesSkipped    int64    `json:"messages_skipped"`
	BytesImported      int64    `json:"bytes_imported"`
	Skipped            []string `json:"skipped,omitempty"`
}

// accountPathSanitize keeps a remote username safe as a single path
// segment (it names the per-account staging subdir).
var accountPathSanitize = regexp.MustCompile(`[^a-zA-Z0-9._@-]+`)

// fetchFn is the fetch step, indirected so tests can drive Import's
// orchestration (staging path + agent dispatch + result merge) without a
// live TLS IMAP transport. Production wiring uses Fetch.
var fetchFn = Fetch

// Import fetches one remote IMAP account into a staging Maildir and hands
// it to the migration.import_mailboxes agent step, which pushes every
// message into the target Stalwart account via JMAP (dedup on Message-ID
// → idempotent on resume). The target jabali mailbox must already exist.
func Import(ctx context.Context, cfg ImportConfig, agentCli agent.AgentInterface) (*ImportResult, error) {
	if cfg.TargetEmail == "" {
		return nil, errors.New("imap: target_email required")
	}
	if cfg.JobID == "" {
		return nil, errors.New("imap: job_id required")
	}
	if cfg.StagingBase == "" {
		return nil, errors.New("imap: staging_base required")
	}
	if agentCli == nil {
		return nil, errors.New("imap: agent client required")
	}

	safeAccount := accountPathSanitize.ReplaceAllString(cfg.Account.Username, "_")
	maildirRoot := filepath.Join(cfg.StagingBase, cfg.JobID, "imap", safeAccount, "Maildir")

	fetchRes, err := fetchFn(ctx, cfg.Account, cfg.Folders, maildirRoot)
	if err != nil {
		return nil, fmt.Errorf("imap: fetch: %w", err)
	}

	raw, err := agentCli.Call(ctx, "migration.import_mailboxes", map[string]any{
		"job_id":       cfg.JobID,
		"src_mail_dir": maildirRoot,
		"owner_email":  cfg.TargetEmail,
	})
	if err != nil {
		return nil, fmt.Errorf("imap: import_mailboxes: %w", err)
	}

	var ag agentImportMailboxesResult
	if err := json.Unmarshal(raw, &ag); err != nil {
		return nil, fmt.Errorf("imap: decode import result: %w", err)
	}

	res := &ImportResult{
		Fetch:            fetchRes,
		MessagesImported: ag.MessagesImported,
		BytesImported:    ag.BytesImported,
	}
	res.Skipped = append(res.Skipped, fetchRes.Skipped...)
	res.Skipped = append(res.Skipped, ag.Skipped...)
	return res, nil
}
