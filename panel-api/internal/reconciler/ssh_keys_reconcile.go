package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// sshKeysDispatchState is what each user's sshKeysDispatchCache entry
// holds. Hash covers the sorted desired key set (or a sentinel for
// "no keys"); At is when we last dispatched the agent for this user.
type sshKeysDispatchState struct {
	Hash string
	At   time.Time
}

// sshKeysReDispatchInterval forces a re-dispatch even when the hash
// matches, so any out-of-band drift in `~/.ssh/authorized_keys`
// (operator hand-edit, agent restart partial state) is corrected on
// a bounded schedule.
const sshKeysReDispatchInterval = 15 * time.Minute

// desiredSSHKeysHash computes a stable hash of the desired authorized_keys
// state. Sorts lines first so the order in the DB doesn't matter; encodes
// the count separately so an empty key-list hashes distinctly from a
// hypothetical zero-length line.
func desiredSSHKeysHash(keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	body := strings.Join(sorted, "\n") + "\nn=" + strconv.Itoa(len(sorted))
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// desiredSSHFullHash combines every user-state input the reconcile pass
// dispatches to the agent: keys + sshEnabled + nspawn pin + username.
// When this hash matches the cached one AND we're within
// sshKeysReDispatchInterval, we skip ALL six per-user agent calls (set_shell,
// home_chown, join/leave sftp group, join/leave sandbox group, write_nspawn_pin,
// authorized_keys write/delete) — not just the keys op the v1 cache covered.
// This was the next per-tick chatter source after PR #61: even with the keys
// op gated, 3 users x 5 unconditional IPCs/tick = 15 agent round-trips/min for
// no diff. Self-heals on the next interval to catch drift (manual chsh, gpasswd).
func desiredSSHFullHash(username string, sshEnabled bool, pin string, keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	parts := []string{
		"u=" + username,
		"ssh=" + strconv.FormatBool(sshEnabled),
		"pin=" + pin,
		"n=" + strconv.Itoa(len(sorted)),
		"k=" + strings.Join(sorted, "\n"),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// ReconcileSSHKeysForUser syncs the user's SSH keys to authorized_keys.
// Skips silently if user has no Linux username (admin-only or pending).
// Adds user to jabali-sftp group, then writes or deletes authorized_keys.
func (r *Reconciler) ReconcileSSHKeysForUser(ctx context.Context, userID string) error {
	// Fetch user to check for Linux username
	user, err := r.users.FindByID(ctx, userID)
	if err != nil {
		if err == repository.ErrNotFound {
			// User doesn't exist; skip silently
			return nil
		}
		return fmt.Errorf("fetch user: %w", err)
	}

	// Skip if user has no Linux username
	if user.Username == nil || *user.Username == "" || user.IsAdmin {
		r.log.DebugContext(ctx, "reconcile ssh keys: skip (no username)", "user_id", userID)
		return nil
	}

	// Compute the full per-user state we'll dispatch (sshEnabled,
	// pin) and the desired key list, BEFORE any agent call. Lets us
	// hash-gate every IPC below — without this, set_shell /
	// home_chown / group_method / sandbox_group / write_nspawn_pin
	// were re-dispatched every tick even when the inputs hadn't
	// changed (3 users x 5 calls/tick = 15 no-op IPCs/min on puzzle).
	sshEnabled := false
	var pkgPin *string
	if r.packages != nil && user.PackageID != nil && *user.PackageID != "" {
		pkg, pkgErr := r.packages.FindByID(ctx, *user.PackageID)
		if pkgErr == nil && pkg != nil {
			sshEnabled = pkg.SSHEnabled
			pkgPin = pkg.NspawnImageVersion
		}
	}

	// Fetch user's SSH keys early so the hash incorporates them too.
	keysEarly, err := r.sshKeys.ListByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("list ssh keys: %w", err)
	}
	desiredLines := make([]string, 0, len(keysEarly))
	for _, key := range keysEarly {
		desiredLines = append(desiredLines, key.PublicKey)
	}

	// Compute the pin we'd actually write so its included in the hash.
	pinPreview := ""
	if pkgPin != nil {
		pinPreview = *pkgPin
	}
	if sshEnabled && pinPreview == "" && r.serverSettings != nil {
		if sset, sErr := r.serverSettings.Get(ctx); sErr == nil && sset != nil && sset.DefaultNspawnImageVersion != "" {
			pinPreview = sset.DefaultNspawnImageVersion
		}
	}
	if !sshEnabled {
		pinPreview = ""
	}

	fullHash := desiredSSHFullHash(*user.Username, sshEnabled, pinPreview, desiredLines)

	// Combined-hash gate. When everything matches AND we re-dispatched
	// within sshKeysReDispatchInterval, skip ALL six agent IPCs below.
	// Drift heals on the next interval pass (15 min force-resync).
	if v, ok := r.sshKeysDispatchCache.Load(userID); ok {
		if st, okT := v.(sshKeysDispatchState); okT &&
			st.Hash == fullHash &&
			time.Since(st.At) < sshKeysReDispatchInterval {
			return nil
		}
	}

	// M13: ensure the wrapper is the user's login shell. Defense-in-depth
	// for SFTP users (ForceCommand internal-sftp wins) and the actual
	// sandbox entry point for SSH-shell users. Idempotent — agent skips
	// chsh when current shell matches.
	if _, err := r.agent.Call(ctx, "ssh.user.set_shell", map[string]interface{}{
		"username": *user.Username,
		"shell":    "/usr/local/bin/jabali-ssh-shell",
	}); err != nil {
		// Don't fail the whole reconcile on shell-set failure — older
		// hosts may not have the wrapper installed yet (jabali update
		// pending). Log and continue so SFTP/auth keys still flow.
		r.log.WarnContext(ctx, "reconcile ssh keys: set_shell failed",
			"user_id", userID, "username", *user.Username, "error", err)
	}

	// Group membership gates SSH access mode:
	//   ssh_enabled=true  -> leave jabali-sftp group -> real shell login
	//   ssh_enabled=false -> join jabali-sftp group  -> SFTP-only (Match block)
	// SSHEnabled lives on the hosting package, not the per-user overrides
	// table. Missing package (no package_id, or package fetch fails)
	// keeps the safe default (SFTP-only).
	// Order matters: when going SFTP→SSH we must restore <u>:<u> 0750 on
	// /home/<u> BEFORE leaving jabali-sftp; when going SSH→SFTP we must
	// flip to root:<u> 0751 BEFORE joining (sshd refuses to chroot into a
	// non-root path on the next connect). Calling home_chown first in both
	// paths is the safe order.
	homeMode := "sftp"
	groupMethod := "ssh.user.join_sftp_group"
	if sshEnabled {
		homeMode = "ssh"
		groupMethod = "ssh.user.leave_sftp_group"
	}
	// GH #1053: FTP/SFTP subaccounts chroot to /home/<u> via their own
	// Match User blocks, and sshd refuses a non-root-owned chroot. A
	// shell-enabled tenant with subaccounts therefore keeps the root:<u>
	// 0751 home (the M12 trade-off: top-level $HOME read-only for the
	// tenant, subdirs untouched) — otherwise this pass and the FTP pass
	// flip ownership back and forth and subaccount logins die right
	// after auth (seen live: FileZilla "remote side unexpectedly closed").
	// Group membership is unaffected: the tenant keeps their shell.
	if homeMode == "ssh" && r.ftpAccounts != nil {
		if accts, ferr := r.ftpAccounts.ListByUserID(ctx, userID); ferr == nil {
			for _, a := range accts {
				if a.IsEnabled && a.SFTPAccess {
					homeMode = "sftp"
					break
				}
			}
		}
	}
	if _, err := r.agent.Call(ctx, "ssh.user.home_chown", map[string]interface{}{
		"username": *user.Username,
		"mode":     homeMode,
	}); err != nil {
		return fmt.Errorf("ssh.user.home_chown: %w", err)
	}
	if _, err := r.agent.Call(ctx, groupMethod, map[string]interface{}{
		"username": *user.Username,
	}); err != nil {
		return fmt.Errorf("%s: %w", groupMethod, err)
	}

	// M13 sandbox group: SSH-shell users need membership in
	// jabali-ssh-sandbox so the sudoers entry permits exec'ing
	// jabali-nspawn-enter. SFTP users are removed.
	sandboxGroupMethod := "ssh.user.leave_sandbox_group"
	if sshEnabled {
		sandboxGroupMethod = "ssh.user.join_sandbox_group"
	}
	if _, err := r.agent.Call(ctx, sandboxGroupMethod, map[string]interface{}{
		"username": *user.Username,
	}); err != nil {
		// Non-fatal: bubblewrap mode (default) doesn't require this
		// group, and the wrapper falls through to nologin if nspawn
		// can't sudo. Log and continue.
		r.log.WarnContext(ctx, "reconcile ssh keys: sandbox group failed",
			"user_id", userID, "username", *user.Username, "method", sandboxGroupMethod, "error", err)
	}

	// M13 nspawn pin (pre-computed as pinPreview above; reuse so the
	// hash and the agent call agree on the exact value).
	if _, err := r.agent.Call(ctx, "ssh.user.write_nspawn_pin", map[string]interface{}{
		"username": *user.Username,
		"image":    pinPreview,
	}); err != nil {
		r.log.WarnContext(ctx, "reconcile ssh keys: write_nspawn_pin failed",
			"user_id", userID, "username", *user.Username, "error", err)
	}

	keys := keysEarly // alias to original name so the rest of the fn unchanged

	if len(keys) > 0 {
		if _, err := r.agent.Call(ctx, "ssh.authorized_keys.write", map[string]interface{}{
			"username": *user.Username,
			"keys":     desiredLines,
		}); err != nil {
			return fmt.Errorf("write authorized_keys: %w", err)
		}
		// Demoted to Debug: this fires every reconcile tick (~60s) for
		// every user with keys, regardless of whether the agent
		// actually rewrote the file. Operators don't need to see a
		// per-tick scroll at INFO; debug surfaces it when investigating.
		r.log.DebugContext(ctx, "reconcile ssh keys: wrote authorized_keys",
			"user_id", userID, "username", *user.Username, "key_count", len(keys))
	} else {
		if _, err := r.agent.Call(ctx, "ssh.authorized_keys.delete", map[string]interface{}{
			"username": *user.Username,
		}); err != nil {
			return fmt.Errorf("delete authorized_keys: %w", err)
		}
		// Demoted to Debug AND reworded. Agent ssh.authorized_keys.delete
		// is the strip-managed-block path (PR #19) — it removes ONLY
		// the jabali marker block, preserving any operator keys
		// outside it (or removes the file if jabali was the sole
		// content). The old "deleted authorized_keys" message at INFO
		// scrolled every ~60s per keyless user and read as if jabali
		// were nuking operator SSH access on every tick — exactly the
		// scar PR #19 fixed in the agent, but the log line never
		// caught up. See [[project_ssh_authorized_keys_destruction]].
		r.log.DebugContext(ctx, "reconcile ssh keys: synced empty key set (jabali managed block cleared; operator keys preserved)",
			"user_id", userID, "username", *user.Username)
	}
	// Record the successful dispatch so the next tick can short-circuit.
	r.sshKeysDispatchCache.Store(userID, sshKeysDispatchState{Hash: fullHash, At: time.Now()})

	return nil
}

// reconcileSSHKeysForAllUsers iterates all users with a username and reconciles their SSH keys.
func (r *Reconciler) reconcileSSHKeysForAllUsers(ctx context.Context) {
	// Skip if SSH keys repository is not initialized
	if r.sshKeys == nil {
		return
	}

	users, _, err := r.users.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.WarnContext(ctx, "reconcile ssh keys for all users: list users", "error", err)
		return
	}

	for i := range users {
		user := &users[i]
		if user.Username == nil || *user.Username == "" || user.IsAdmin {
			continue // Skip users without a Linux username
		}

		userCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := r.ReconcileSSHKeysForUser(userCtx, user.ID)
		cancel()

		if err != nil {
			r.log.WarnContext(ctx, "reconcile ssh keys: per-user error",
				"user_id", user.ID, "username", *user.Username, "error", err)
		}
	}
}
