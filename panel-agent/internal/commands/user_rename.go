// user.rename — rename a tenant's Linux account in place (GH #1238).
//
// The uid is the stable anchor: usermod -l keeps it, so NO file re-chown is
// needed — only the account NAME and the home PATH change. Both homes live under
// /home, so the `usermod -m` move is always same-filesystem (instant rename(2)).
//
// Sequence: quiesce every name-keyed per-user service (FPM pools, nspawn unit,
// slice), disable linger, kill lingering processes, THEN usermod -l / groupmod -n
// / usermod -d -m, reap the orphaned crontab (usermod does not move it), and
// re-provision the slice + FPM drop-ins under the new name. Idempotent: if the new
// name already exists and the old is gone, it is a no-op success.
//
// The panel side (userops.RenameUser) refuses the rename up front when the tenant
// has isolated FTP jails (bind-mounts under the home) or app installs — those need
// the mount/credential handling that a later version will add. This verb assumes
// that gate has passed.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type userRenameParams struct {
	OldUsername string `json:"old_username"`
	NewUsername string `json:"new_username"`
	UID         int    `json:"uid"`
}

type userRenameResponse struct {
	OldUsername string `json:"old_username"`
	NewUsername string `json:"new_username"`
	UID         int    `json:"uid"`
	NewHome     string `json:"new_home"`
	AlreadyDone bool   `json:"already_done"`
}

// runCombined runs a command through the exec seam and returns trimmed combined
// output plus the error — for surfacing usermod/groupmod stderr in AgentErrors.
func runCombined(ctx context.Context, name string, args ...string) (string, error) {
	out, err := execCommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func userRenameHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userRenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if !usernameRegex.MatchString(p.OldUsername) || !usernameRegex.MatchString(p.NewUsername) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "old_username and new_username must match ^[a-z_][a-z0-9_-]{0,31}$"}
	}
	if p.OldUsername == p.NewUsername {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "old and new username are identical"}
	}
	newHome := "/home/" + p.NewUsername

	oldExists := execCommandContext(ctx, "id", p.OldUsername).Run() == nil
	newExists := execCommandContext(ctx, "id", p.NewUsername).Run() == nil

	// Idempotent: the rename already happened.
	if newExists && !oldExists {
		return userRenameResponse{OldUsername: p.OldUsername, NewUsername: p.NewUsername, UID: p.UID, NewHome: newHome, AlreadyDone: true}, nil
	}
	if !oldExists {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("user %q not found", p.OldUsername)}
	}
	if newExists {
		return nil, &agentwire.AgentError{Code: agentwire.CodeAlreadyExists, Message: fmt.Sprintf("target user %q already exists", p.NewUsername)}
	}

	// --- Quiesce every name-keyed per-user service under the OLD name, while the
	// old name still resolves (slice removal needs the UID resolvable) ---
	reapUserFPMPools(ctx, p.OldUsername)
	reapUserNspawnPHPUnit(ctx, p.OldUsername)
	sliceParams, _ := json.Marshal(map[string]string{"username": p.OldUsername})
	if _, err := userSliceRemoveHandler(ctx, sliceParams); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("could not remove the old per-user slice before rename: %v", err)}
	}
	// Disable linger so no user-manager holds the account; best-effort.
	if err := execCommandContext(ctx, "loginctl", "disable-linger", p.OldUsername).Run(); err != nil {
		slog.InfoContext(ctx, "user.rename: loginctl disable-linger failed (best-effort)", "username", p.OldUsername, "err", err)
	}
	// Kill any remaining tenant processes so usermod -l is not refused
	// ("user is currently used by process N"). uid-scoped: only the tenant's own
	// processes, never the agent or panel. TERM, brief settle, then KILL.
	if p.UID > 0 {
		_ = execCommandContext(ctx, "pkill", "-TERM", "-u", strconv.Itoa(p.UID)).Run()
		time.Sleep(500 * time.Millisecond)
		_ = execCommandContext(ctx, "pkill", "-KILL", "-u", strconv.Itoa(p.UID)).Run()
	}

	// --- Rename the account, its primary group, and move the home ---
	if out, err := runCombined(ctx, "usermod", "-l", p.NewUsername, p.OldUsername); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("usermod -l failed (account still %q; re-run to resume): %v: %s", p.OldUsername, err, out)}
	}
	// Debian useradd creates a per-user group named after the user; rename it too
	// so the group name tracks the account. Best-effort: if no such group exists
	// (shared-group setups), skip without failing the rename.
	if execCommandContext(ctx, "getent", "group", p.OldUsername).Run() == nil {
		if out, err := runCombined(ctx, "groupmod", "-n", p.NewUsername, p.OldUsername); err != nil {
			slog.WarnContext(ctx, "user.rename: groupmod -n failed (best-effort)", "old", p.OldUsername, "new", p.NewUsername, "err", err, "out", out)
		}
	}
	// Move the home dir. Same filesystem (both under /home) → instant.
	if out, err := runCombined(ctx, "usermod", "-d", newHome, "-m", p.NewUsername); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("usermod -d -m failed (account renamed to %q but home not moved; re-run to resume): %v: %s", p.NewUsername, err, out)}
	}

	// usermod does not move the crontab spool file; reap the orphan (cron
	// reconcile re-applies the jobs under the new name).
	if err := os.Remove("/var/spool/cron/crontabs/" + p.OldUsername); err != nil && !os.IsNotExist(err) {
		slog.InfoContext(ctx, "user.rename: could not remove old crontab spool (best-effort)", "username", p.OldUsername, "err", err)
	}

	// --- Re-provision per-user services under the NEW name ---
	newSliceParams, _ := json.Marshal(map[string]string{"username": p.NewUsername})
	if _, err := userSliceEnsureHandler(ctx, newSliceParams); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("account renamed to %q but re-provisioning its slice failed (re-run to resume): %v", p.NewUsername, err)}
	}
	if err := execCommandContext(ctx, "loginctl", "enable-linger", p.NewUsername).Run(); err != nil {
		slog.InfoContext(ctx, "user.rename: loginctl enable-linger failed (best-effort)", "username", p.NewUsername, "err", err)
	}

	// Confirm the uid is unchanged (the whole design depends on it).
	uid := p.UID
	if out, err := execCommandContext(ctx, "id", "-u", p.NewUsername).Output(); err == nil {
		if got, perr := strconv.Atoi(strings.TrimSpace(string(out))); perr == nil {
			uid = got
			if p.UID > 0 && got != p.UID {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("uid changed during rename (%d → %d) — aborting; this must never happen", p.UID, got)}
			}
		}
	}

	return userRenameResponse{OldUsername: p.OldUsername, NewUsername: p.NewUsername, UID: uid, NewHome: newHome}, nil
}

func init() {
	Default.Register("user.rename", userRenameHandler)
}
