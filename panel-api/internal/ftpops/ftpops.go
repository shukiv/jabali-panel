// Package ftpops is the transport-neutral FTP Account Lifecycle Module
// (JAB-276). It owns the mutation ordering, the Agent calls, and the immediate
// SFTP convergence for FTP/SFTP subaccounts, so the admin and tenant adapters
// only resolve authorization (which account, which owner) and map the result to
// their own transport (ADR-0083).
//
// Every lifecycle operation runs here: Create, UpdateAccess, SetPassword,
// Delete, and ReapOwner (owner cleanup). Admin and tenant doors call the same
// implementation, so their state-transition transcripts are identical by
// construction (AC1), and every delete path shares one host teardown
// (deleteHostAlias) while keeping its own failure policy.
package ftpops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ftpsync"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// AgentTimeout bounds every host call the module makes.
const AgentTimeout = 30 * time.Second

// ErrPersist wraps a desired-state (database) write failure. Adapters map it to
// an internal error; the wrapped repository error stays in the chain.
var ErrPersist = errors.New("ftpops: persist failed")

// Deps are the collaborators the lifecycle operations need.
type Deps struct {
	Agent    agent.AgentInterface
	Accounts repository.FtpAccountRepository
	Users    repository.UserRepository
	Packages ftpsync.PackageGetter
	Log      *slog.Logger
	// QuotaMount is the filesystem mount /home lives on, required for GH #1145
	// isolated accounts (per-uid setquota); empty refuses an isolated create.
	QuotaMount string
}

// UpdateAccess persists acct's access flags (already applied by the adapter)
// and then converges the host.
//
// Ordering (JAB-269 / JAB-276 AC2): the desired state is persisted FIRST, on the
// caller's context — a cancellation before the commit aborts having changed
// nothing, and the host can only ever lag the database, never run ahead of it.
// Once the row is durable, the host apply runs on a context detached from
// cancellation so a client disconnect cannot abort it half-way.
//
// The sshd drop-in is always re-rendered from the committed database, even when
// set_access fails: for a disable, removing the SFTP Match block IS the
// revocation, so it still revokes when the unix lock failed (defense in depth).
//
// Returns an ErrPersist-wrapped error when the row write fails (no host call is
// made), or the raw set_access error after the sync ran (the row is deliberately
// not reverted — the reconciler converges the host to it on its next tick).
func UpdateAccess(ctx context.Context, d Deps, acct *models.FtpAccount, tenantUsername string) error {
	acct.UpdatedAt = time.Now().UTC()
	if err := d.Accounts.Update(ctx, acct); err != nil {
		return fmt.Errorf("%w: %w", ErrPersist, err)
	}
	hostCtx := context.WithoutCancel(ctx)
	setErr := agentCall(hostCtx, d.Agent, "ftpaccount.set_access", map[string]any{
		"tenant_username": tenantUsername,
		"username":        acct.Username,
		"ftp_access":      acct.FTPAccess,
		"webdav_access":   acct.WebDAVAccess,
		"enabled":         acct.IsEnabled,
	})
	syncHostAccess(hostCtx, d, tenantUsername)
	return setErr
}

// Delete removes the host alias and then the row.
//
// Host first: the row is the only handle, so it must outlive the host alias — a
// failed host delete keeps the row for the retry, while the reverse order would
// strand an unaccounted host credential.
//
// Once the host alias is gone, the row delete and the sshd re-render run on a
// context detached from cancellation. A client disconnect at that instant used
// to abort the row delete, leaving a row with no alias — which the reconciler
// re-provisions with a throwaway password, resurrecting a deleted account.
//
// Returns the raw agent error when the host delete fails (row kept, no sync), or
// an ErrPersist-wrapped error when the row delete fails (no sync).
func Delete(ctx context.Context, d Deps, acct *models.FtpAccount, tenantUsername string) error {
	if err := deleteHostAlias(ctx, d, tenantUsername, acct.Username); err != nil {
		return err
	}
	afterHost := context.WithoutCancel(ctx)
	if err := d.Accounts.Delete(afterHost, acct.ID); err != nil {
		return fmt.Errorf("%w: %w", ErrPersist, err)
	}
	syncHostAccess(afterHost, d, tenantUsername)
	return nil
}

// deleteHostAlias tears down one subaccount's host alias (userdel + isolated
// jail unmount/remove). Every delete path — the adapter doors and the owner
// cleanup — goes through it.
func deleteHostAlias(ctx context.Context, d Deps, tenantUsername, username string) error {
	return agentCall(ctx, d.Agent, "ftpaccount.delete", map[string]any{
		"tenant_username": tenantUsername,
		"username":        username,
	})
}

func agentCall(ctx context.Context, ag agent.AgentInterface, method string, params map[string]any) error {
	if ag == nil {
		return errors.New("agent unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, AgentTimeout)
	defer cancel()
	_, err := ag.Call(callCtx, method, params)
	return err
}

func syncHostAccess(ctx context.Context, d Deps, tenantUsername string) {
	ftpsync.SyncFtpHostAccess(ctx, d.Agent, d.Accounts, d.Users, d.Packages, d.Log, tenantUsername)
}
