package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailboxops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// mailbox_ops.go mirrors the HTTP mailbox handlers but goes straight to
// the DB + agent the way `cli_ops.go` does for users/domains. The CLI
// bypasses Kratos auth because it runs in the `jabali` group on-box, so
// there's no per-user claims resolution — ownership checks collapse to
// "admin", which is fine for operator-only tooling.
//
// The create/rotate/quota/delete logic + the quota/bcrypt constants now live in
// the shared mailboxops package (JAB-291); these helpers are thin CLI wrappers
// over it, so the CLI and the REST handlers can no longer drift. Only the
// CLI-local agent timeout stays here.
const cliMailboxAgentTimeout = 30 * time.Second

// agentNotifier is the minimal surface the CLI needs from the agent
// client. Small interface so tests can pass a recording stub without
// pulling in the full *agent.Client. Matches ADR-0013 best-effort
// semantics: errors here don't fail the command.
type agentNotifier func(ctx context.Context, cmd string, params any)

// mailboxRepoFromDB is the CLI-side constructor. Mirrors
// domainRepoFromDB / packageRepoFromDB in root.go.
func mailboxRepoFromDB() repository.MailboxRepository {
	return repository.NewMailboxRepository(sharedDB)
}

// ssoKeyForCLI loads /etc/jabali-panel/sso.key on demand. Returns nil
// when the key isn't configured — callers pass that through to
// create/rotate which then skip ciphering plaintext into password_enc
// and leave the webmail SSO feature unavailable for rows touched via
// CLI. On a healthy install (install.sh has always generated sso.key
// since M7 shipped), this returns a valid key.
func ssoKeyForCLI() *ssokey.Key {
	if sharedCfg == nil || sharedCfg.SSO.KeyPath == "" {
		return nil
	}
	return loadSSOKey(sharedCfg.SSO.KeyPath, sharedLog)
}

// resolveDomainSpec accepts either a domain name (preferred CLI UX) or
// a ULID and returns the Domain row.
//
// The name path is primary: `jabali mailbox create --domain example.com`
// reads better than an opaque ULID. ULID form is handy for scripts
// piping `jabali domain list --json` output.
func resolveDomainSpec(ctx context.Context, domains repository.DomainRepository, spec string) (*models.Domain, error) {
	if spec == "" {
		return nil, fmt.Errorf("domain spec is empty")
	}
	// Try ID first (ULIDs are exactly 26 chars of Crockford base32; bare
	// names never look like that).
	if len(spec) == 26 {
		if d, err := domains.FindByID(ctx, spec); err == nil {
			return d, nil
		} else if !errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("lookup domain by id: %w", err)
		}
	}
	d, err := domains.FindByName(ctx, spec)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("domain %q not found", spec)
		}
		return nil, fmt.Errorf("lookup domain by name: %w", err)
	}
	return d, nil
}

// listMailboxesDirect returns every mailbox in `domainID`. Page size is
// 1000 — matches listDomainsDirect / listUsersDirect. Caller formats.
func listMailboxesDirect(ctx context.Context, repo repository.MailboxRepository, domainID string) ([]models.Mailbox, error) {
	rows, _, err := repo.ListByDomainID(ctx, domainID, repository.ListOptions{Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}
	return rows, nil
}

// createMailboxDirect mirrors POST /domains/:id/mailboxes:
//   - canonicalise the local part via mailaddr.Canonicalise
//   - enforce ExistsByDomainAndLocalPart uniqueness
//   - generate ULID password if caller didn't pass one
//   - bcrypt the password
//   - Create() then fire mailbox.create agent RPC best-effort (ADR-0013)
//
// When `ssoKey` is non-nil the plaintext is also sealed into
// password_enc so the webmail SSO endpoint can decrypt it later.
// Pre-Step-8 mailboxes (ssoKey=nil) still work — the SSO mint
// surfaces a typed error until the next rotate with a live ssoKey.
//
// Returns the row plus the generated password, which is empty when the
// caller supplied one — the CLI layer owns the reveal-once printing
// contract.
func createMailboxDirect(ctx context.Context, repo repository.MailboxRepository, notify agentNotifier, ssoKey *ssokey.Key, dom *models.Domain, localPart, password string, quotaBytes uint64, displayName string, sendOnly bool) (*models.Mailbox, string, error) {
	return mailboxops.Create(ctx, mailboxops.Deps{Mailboxes: repo, SSOKey: ssoKey}, mailboxops.CreateInput{
		Domain:      dom,
		LocalPart:   localPart,
		Password:    password,
		QuotaBytes:  quotaBytes,
		DisplayName: displayName,
		SendOnly:    sendOnly,
	}, mailboxops.NotifyFunc(notify))
}

// deleteMailboxDirect mirrors DELETE /mailboxes/:mbid: agent call first
// (it owns the RocksDB-side destroy; failure means we bail BEFORE the
// DB delete so Stalwart state matches), then the row.
//
// Unlike create/set-quota/rotate-password, the agent call is a HARD
// dependency here — the Stalwart account must be destroyed first or we
// end up with a tombstoned DB row whose Stalwart side is still valid.
// So this helper takes an agent-call func that returns an error rather
// than the fire-and-forget agentNotifier.
//
// agentCaller's return mirrors agent.Client.Call: raw bytes + error.
// Delete discards the bytes; email-enable unmarshals them for the DKIM
// fields (see domain_email_cmd.go).
type agentCaller func(ctx context.Context, cmd string, params any) (json.RawMessage, error)

func deleteMailboxDirect(ctx context.Context, repo repository.MailboxRepository, call agentCaller, email string) error {
	return mailboxops.Delete(ctx, repo, mailboxops.CallFunc(call), email)
}

// setMailboxQuotaDirect mirrors PATCH /mailboxes/:mbid. Quota floor
// check matches the HTTP handler (16 MiB).
func setMailboxQuotaDirect(ctx context.Context, repo repository.MailboxRepository, notify agentNotifier, email string, quotaBytes uint64) (*models.Mailbox, error) {
	return mailboxops.SetQuota(ctx, mailboxops.Deps{Mailboxes: repo}, email, quotaBytes, mailboxops.NotifyFunc(notify))
}

// rotateMailboxPasswordDirect mirrors POST /mailboxes/:mbid/rotate-password.
// Empty `newPassword` → generate a ULID and return it once. Writes the
// AES-256-GCM envelope too when ssoKey is non-nil so the webmail SSO
// flow stays in sync with the bcrypt hash.
func rotateMailboxPasswordDirect(ctx context.Context, repo repository.MailboxRepository, notify agentNotifier, ssoKey *ssokey.Key, email, newPassword string) (string, error) {
	return mailboxops.RotatePassword(ctx, mailboxops.Deps{Mailboxes: repo, SSOKey: ssoKey}, email, newPassword, mailboxops.NotifyFunc(notify))
}

// notifyAgentMailbox is the production agentNotifier wired off the
// global sharedAgent. Swallows errors — ADR-0013 best-effort.
func notifyAgentMailbox(ctx context.Context, cmd string, params any) {
	if sharedAgent == nil {
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, cliMailboxAgentTimeout)
	defer cancel()
	_, _ = sharedAgent.Call(agentCtx, cmd, params)
}

// callAgentMailbox is the production agentCaller — used by delete
// (hard dependency) and email-enable (needs the body). Surfaces the
// error AND the raw payload back up.
func callAgentMailbox(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
	if sharedAgent == nil {
		return nil, fmt.Errorf("agent not configured")
	}
	agentCtx, cancel := context.WithTimeout(ctx, cliMailboxAgentTimeout)
	defer cancel()
	return sharedAgent.Call(agentCtx, cmd, params)
}
