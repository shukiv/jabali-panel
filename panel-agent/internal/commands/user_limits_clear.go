package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
)

// user.limits.clear — removes the M18 limits drop-in and clears the
// POSIX user quota. Called on user delete and on override-revert
// workflows. Idempotent: safe to call when the drop-in doesn't exist
// and when no quota is currently set for the user.

type userLimitsClearParams struct {
	Username   string `json:"username"`
	QuotaMount string `json:"quota_mount"` // empty skips the setquota step
}

type userLimitsClearResponse struct {
	Username      string `json:"username"`
	DropinRemoved bool   `json:"dropin_removed"`
	QuotaCleared  bool   `json:"quota_cleared"`
	// QuotaSkippedReason: empty on success; "quota_disabled_on_fs"
	// when the host's filesystem doesn't have quotas enabled (the
	// common operator-disabled-disk_quota_enabled case). Lets the
	// panel surface state in diagnostics without re-deriving it.
	QuotaSkippedReason string `json:"quota_skipped_reason,omitempty"`
	// KernelMismatch: the running slice still holds a limit after the
	// clear (see convergeLiveLimits). Empty when it is unlimited or idle.
	KernelMismatch string `json:"kernel_mismatch,omitempty"`
}

func userLimitsClearHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userLimitsClearParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if !userSliceUsernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid username %q", p.Username),
		}
	}

	testMutex.Lock()
	systemdRootFn := systemdRoot
	runCmdFn := runCmd
	testMutex.Unlock()
	root := systemdRootFn()

	dropinPath := filepath.Join(root, fmt.Sprintf("jabali-user-%s.slice.d", p.Username), "limits.conf")
	resp := &userLimitsClearResponse{Username: p.Username}

	if err := os.Remove(dropinPath); err == nil {
		resp.DropinRemoved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("remove drop-in: %v", err),
		}
	}

	// SIGHUP + sleep only when we actually removed a drop-in.
	// Reconciler calls user.limits.clear per-tick for every user
	// without a package; sending SIGHUP 1 per call when the drop-in
	// has been absent forever wastes a systemd daemon-reload + 250ms
	// sleep per user per tick (puzzle: 3 users x 60 ticks/hr = 180
	// no-op SIGHUPs/hr after PR #78 still left this path leaking).
	// File-absent case is identical pre/post = no kernel change to
	// reload for.
	if resp.DropinRemoved {
		if err := killProcess(1, syscall.SIGHUP); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("kill -HUP 1: %v", err),
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	// The reload above leaves a running slice on its old limits; reset them
	// now after a removal, and on every other call only when the kernel
	// still holds one.
	if root == "/etc/systemd/system" {
		resp.KernelMismatch = convergeLiveLimits(ctx, runCmdFn, p.Username, limits.EffectiveLimits{}, resp.DropinRemoved)
	}

	if p.QuotaMount != "" {
		// Clear the quota: 0 0 0 0 means no soft, no hard, no inode
		// limits — user becomes unbounded on this mount.
		_, stderr, err := runCmdFn(ctx, "setquota",
			"-u", p.Username,
			"0", "0", "0", "0",
			p.QuotaMount,
		)
		if err != nil {
			// Tolerate the benign "quota not enabled on this FS"
			// failure: many hosts run with disk_quota_enabled=0 (and
			// /home mounted `noquota`), where setquota cannot operate
			// at all. The reconciler dispatches user.limits.clear
			// every ~60s per user — without this guard, every tick
			// emitted a WARN line per user in `journalctl
			// -u jabali-panel`. Soft-success when stderr matches a
			// known quota-disabled signature; real failures still
			// surface as CodeInternal.
			lower := strings.ToLower(string(stderr))
			// Real-shape patterns from setquota across versions/distros.
			// Add new strings here (lowercased) as ops encounter them.
			quotaDisabled :=
				strings.Contains(lower, "no quota enabled") ||
					strings.Contains(lower, "not all specified mountpoints are using quota") ||
					strings.Contains(lower, "cannot find filesystem mount point") ||
					strings.Contains(lower, "error getting quota information") ||
					strings.Contains(lower, "quota was not enabled") ||
					strings.Contains(lower, "quota file not found") ||
					strings.Contains(lower, "not a quotacommand") ||
					(strings.Contains(lower, "no such file or directory") && strings.Contains(lower, "aquota"))
			if quotaDisabled {
				resp.QuotaCleared = false
				resp.QuotaSkippedReason = "quota_disabled_on_fs"
				return resp, nil
			}
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("setquota clear: %v", err),
			}
		}
		resp.QuotaCleared = true
	}

	return resp, nil
}

func init() {
	Default.Register("user.limits.clear", userLimitsClearHandler)
}
