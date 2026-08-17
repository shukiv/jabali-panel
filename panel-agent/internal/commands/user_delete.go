package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// userDeleteParams is the input shape for user.delete.
type userDeleteParams struct {
	Username   string `json:"username"`
	RemoveHome bool   `json:"remove_home"`
}

// userDeleteResponse is the output shape for user.delete.
type userDeleteResponse struct {
	Username    string `json:"username"`
	RemovedHome bool   `json:"removed_home"`
}

// protectedUsers is a hardcoded deny list of users that must never be deleted.
var protectedUsers = map[string]bool{
	"root":   true,
	"jabali": true,
	// Add the service user name here if different from "jabali"
}

func userDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate username format.
	if !usernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid username %q: must match ^[a-z_][a-z0-9_-]{0,31}$", p.Username),
		}
	}

	// Check if user is protected.
	if protectedUsers[p.Username] {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("cannot delete protected user %q", p.Username),
		}
	}

	// Check if user exists.
	checkCmd := execCommandContext(ctx, "id", p.Username)
	if err := checkCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("user %q does not exist", p.Username),
		}
	}

	// GH #686: tear down the user's FPM pool(s) before removing the account so
	// no orphaned version-pin file / jabali-fpm@<user> master is left behind (a
	// stale pin file inflates the PHP Manager "FPM workers" count). Best-effort;
	// the reconciler's orphan reaper (php.pool.reap-orphans) is the backstop.
	reapUserFPMPools(ctx, p.Username)

	// Remove the per-user slice BEFORE userdel so systemd can still resolve the UID
	// while stopping user@<uid>.service.
	sliceParams, _ := json.Marshal(map[string]string{"username": p.Username}) // GH #694: Marshal, not string-concat
	_, sliceErr := userSliceRemoveHandler(ctx, sliceParams)
	if sliceErr != nil {
		var ae *agentwire.AgentError
		if ok := errors.As(sliceErr, &ae); ok {
			slog.InfoContext(ctx, "slice removal failed; aborting user deletion",
				"username", p.Username, "slice_error", ae.Message)
		} else {
			slog.InfoContext(ctx, "slice removal failed; aborting user deletion",
				"username", p.Username, "slice_error", sliceErr.Error())
		}
		return nil, sliceErr
	}
	slog.InfoContext(ctx, "user slice removed successfully", "username", p.Username)

	// Disable systemd-user linger so userdel can clean up the user-systemd
	// state. user.create enables linger; without disabling here, userdel
	// trips on the /var/lib/systemd/linger/<user> flag and exits 12
	// ("can't remove home directory") even after slice removal (mx scar
	// 2026-04-30).
	if err := execCommandContext(ctx, "loginctl", "disable-linger", p.Username).Run(); err != nil {
		// Best-effort. Linger may already be off.
		slog.InfoContext(ctx, "loginctl disable-linger failed (best-effort)",
			"username", p.Username, "err", err.Error())
	}

	// Delete user. Capture stderr for diagnostics.
	var deleteCmd *exec.Cmd
	if p.RemoveHome {
		deleteCmd = execCommandContext(ctx, "userdel", "--remove", p.Username)
	} else {
		deleteCmd = execCommandContext(ctx, "userdel", p.Username)
	}
	var stderr bytes.Buffer
	deleteCmd.Stderr = &stderr

	if err := deleteCmd.Run(); err != nil {
		// userdel(8) exit 12 = "can't remove home directory". The /etc/passwd
		// + /etc/shadow + /etc/group entries are already gone by the time
		// userdel reaches the home-removal step. Fall back to manual rm so
		// the operator's --purge intent is honored.
		var exitErr *exec.ExitError
		if p.RemoveHome && errors.As(err, &exitErr) && exitErr.ExitCode() == 12 {
			home := "/home/" + p.Username
			if rmErr := os.RemoveAll(home); rmErr != nil {
				return nil, &agentwire.AgentError{
					Code: agentwire.CodeInternal,
					Message: fmt.Sprintf("userdel exit 12; manual rm /home/%s failed: %v (stderr=%q)",
						p.Username, rmErr, stderr.String()),
				}
			}
			slog.InfoContext(ctx, "userdel exit 12; home removed via fallback rm",
				"username", p.Username, "home", home, "userdel_stderr", stderr.String())
			return userDeleteResponse{
				Username:    p.Username,
				RemovedHome: true,
			}, nil
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("userdel failed: %v (stderr=%q)", err, stderr.String()),
		}
	}

	return userDeleteResponse{
		Username:    p.Username,
		RemovedHome: p.RemoveHome,
	}, nil
}

func init() {
	Default.Register("user.delete", userDeleteHandler)
}
