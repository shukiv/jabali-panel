// Package sshkeyops is the shared SSH-key lifecycle (ADR-0083, JAB-292): one
// implementation of key normalization + strength/type validation (via
// internal/sshkeys), fingerprinting, duplicate detection, persistence,
// owner-scoped deletion, and per-user convergence scheduling — that the REST
// handler (internal/api/ssh_keys.go) and the operator CLI
// (cmd/server/sshkey_cmd.go) both route through, so validation and persistence
// can't drift between them. ADR-0083 named this package as promised-but-unbuilt
// alongside dbops/cronops.
//
// Per ADR-0083 the module is transport-agnostic: no net/http, no cobra, no
// os.Exit. Convergence is fire-and-forget through the Scheduler interface (the
// REST adapter wraps the in-process reconciler's async, detached
// ReconcileSSHKeysForUser; the CLI wraps a no-op because a short-lived CLI
// process has no running reconciler — its authorized_keys land on the
// reconciler's next ≤60s tick). Keeping scheduling behind the interface makes
// it the single coalescing point a future restore-batch slice can reuse.
//
// Restore paths use the explicit RestoreBatch operation (below): the HTTP/CLI
// account restore (internal/backupmetadata/apply.go) is routed through it, so it
// no longer writes ssh_keys rows directly. Still direct: the cPanel migration
// importer (internal/migrate/cpanel/restore_sshkeys.go) — its duplicate
// detection string-matches the raw driver error ("Duplicate entry"/"1062")
// rather than repository.ErrConflict, so routing it through RestoreBatch (which
// maps only ErrConflict) changes its cross-job conflict behavior and needs its
// own pass. JAB-292 stays a module-parent until it too uses RestoreBatch (AC4).
package sshkeyops

import (
	"context"
	"errors"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sshkeys"
)

// Scheduler requests per-user authorized_keys convergence after a successful
// mutation. Fire-and-forget: implementations must not block the caller (the
// REST adapter dispatches an async, detached reconcile; the CLI is a no-op).
// Declared here rather than importing the reconciler so this neutral module has
// no dependency on the reconciler or api packages.
type Scheduler interface {
	ScheduleUser(userID string)
}

// Deps is the collaborator set the lifecycle needs. Scheduler is required —
// both adapters wire one (the CLI a no-op); the nil guard is defensive so a
// mis-wired caller degrades to "no immediate convergence" rather than panicking.
type Deps struct {
	Keys      repository.SSHKeyRepository
	Scheduler Scheduler
}

var (
	// ErrDuplicate wraps repository.ErrConflict: a key with the same
	// fingerprint already exists for the owner.
	ErrDuplicate = errors.New("sshkeyops: duplicate key")
	// ErrNotFound covers both "no such key" and "not owned by the caller" —
	// deliberately indistinguishable so an owner-scoped delete can't probe
	// another user's key IDs.
	ErrNotFound = errors.New("sshkeyops: key not found")
)

// AddRequest is the input to Add.
type AddRequest struct {
	UserID    string
	Name      string
	PublicKey string
}

// Add validates + fingerprints the public key, persists it, and schedules a
// per-user reconcile. Validation errors are the internal/sshkeys sentinels
// (ErrInvalidKeyFormat / ErrRSATooWeak / ErrUnsupportedType), returned as-is so
// both adapters map them to their transport without re-declaring the taxonomy.
// A repository conflict becomes ErrDuplicate. Scheduling happens only after the
// row persists — a failed persist schedules nothing.
func Add(ctx context.Context, d Deps, req AddRequest) (*models.SSHKey, error) {
	normalized, fingerprint, err := sshkeys.ParseAndFingerprint(req.PublicKey)
	if err != nil {
		return nil, err // sshkeys.Err* sentinel, unwrapped for errors.Is
	}
	key := &models.SSHKey{
		ID:          ids.NewULID(),
		UserID:      req.UserID,
		Name:        req.Name,
		PublicKey:   normalized,
		Fingerprint: fingerprint,
	}
	if err := d.Keys.Create(ctx, key); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, ErrDuplicate
		}
		return nil, err
	}
	d.schedule(key.UserID)
	return key, nil
}

// FindRequest identifies a key, optionally owner-scoped. When OwnerID is
// non-empty the lookup is scoped to that owner (the REST path passes the
// caller's ID); empty means unscoped/admin (the operator CLI).
type FindRequest struct {
	KeyID   string
	OwnerID string
}

// Find looks up a key with FindRequest's owner-scope rules without deleting it,
// so an adapter can render a confirmation prompt or a delete's 404/500 split.
// ErrNotFound covers both missing and not-owned.
func Find(ctx context.Context, d Deps, req FindRequest) (*models.SSHKey, error) {
	var (
		key *models.SSHKey
		err error
	)
	if req.OwnerID != "" {
		key, err = d.Keys.FindByIDAndUserID(ctx, req.KeyID, req.OwnerID)
	} else {
		key, err = d.Keys.FindByID(ctx, req.KeyID)
	}
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return key, nil
}

// RemoveKey deletes an already-found key and schedules a reconcile for the
// key's owner (not the caller). Takes the row from Find so an adapter that
// needs it for a prompt or an error split doesn't look it up twice. A failed
// delete schedules nothing.
func RemoveKey(ctx context.Context, d Deps, key *models.SSHKey) error {
	if err := d.Keys.Delete(ctx, key.ID); err != nil {
		return err
	}
	d.schedule(key.UserID)
	return nil
}

func (d Deps) schedule(userID string) {
	if d.Scheduler != nil {
		d.Scheduler.ScheduleUser(userID)
	}
}

// RestoreBatch is the explicit restore operation (JAB-292 AC4): the single seam
// a restore path uses to re-insert many SSH keys and then converge each affected
// owner's authorized_keys exactly once.
//
// Unlike Add, Restore does NOT parse or re-fingerprint — it persists the row as
// given, because a restore trusts its source (a backup manifest carries the
// original ID + fingerprint; a migration importer has already parsed and
// fingerprinted). Scheduling is coalesced: however many keys a single owner
// gains, Flush schedules that owner once, and only if at least one of their rows
// actually persisted (a batch of nothing-but-conflicts schedules nothing).
//
// A caller restoring under a live reconciler passes a real Scheduler for
// immediate, batched convergence; a caller whose convergence is tick-based (the
// backup account restore, which re-converges all restored state on the
// reconciler's next tick) or which runs without a reconciler passes a nil
// Scheduler and Flush is a no-op.
type RestoreBatch struct {
	d      Deps
	owners map[string]struct{}
}

// NewRestoreBatch starts an empty batch bound to d. Deps.Scheduler may be nil
// (tick-based / no reconciler), in which case Flush schedules nothing.
func NewRestoreBatch(d Deps) *RestoreBatch {
	return &RestoreBatch{d: d, owners: map[string]struct{}{}}
}

// Restore persists one already-built row. A repository conflict (the key already
// exists for the owner) becomes ErrDuplicate so the caller records it as
// already-present and keeps going; any other persistence error is returned
// unwrapped. Only a successful persist marks the owner for convergence, so a
// failed or duplicate row never schedules anything.
func (b *RestoreBatch) Restore(ctx context.Context, row *models.SSHKey) error {
	if err := b.d.Keys.Create(ctx, row); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return ErrDuplicate
		}
		return err
	}
	b.owners[row.UserID] = struct{}{}
	return nil
}

// Flush schedules convergence exactly once for every owner that had at least one
// row persisted since the last Flush, then clears the set. Idempotent: a second
// Flush with no new successful Restore schedules nothing.
func (b *RestoreBatch) Flush() {
	for userID := range b.owners {
		b.d.schedule(userID)
	}
	b.owners = map[string]struct{}{}
}
