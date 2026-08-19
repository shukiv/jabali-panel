package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// userSliceRemoveParams is the input shape for user.slice.remove.
type userSliceRemoveParams struct {
	Username string `json:"username"`
}

// userSliceRemoveResponse is the output shape for user.slice.remove.
type userSliceRemoveResponse struct {
	Username      string `json:"username"`
	Removed       bool   `json:"removed"`
	AlreadyAbsent bool   `json:"already_absent"`
}

func userSliceRemoveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userSliceRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate username format: ^[a-z][a-z0-9_-]{0,31}$
	if !userSliceUsernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid username %q: must match ^[a-z][a-z0-9_-]{0,31}$", p.Username),
		}
	}

	testMutex.Lock()
	systemdRootFn := systemdRoot
	runCmdFn := runCmd
	testMutex.Unlock()

	root := systemdRootFn()

	// Attempt to resolve uid for login dropin cleanup
	var uid int
	stdout, _, err := runCmdFn(ctx, "id", "-u", p.Username)
	if err == nil {
		if u, parseErr := strconv.Atoi(strings.TrimSpace(string(stdout))); parseErr == nil {
			uid = u
		}
	}

	// Stop FPM service (ignore "not loaded" errors)
	_, _, _ = runCmdFn(ctx, "systemctl", "stop", fmt.Sprintf("jabali-fpm@%s.service", p.Username))

	// Disable FPM service (ignore "not enabled" errors)
	_, _, _ = runCmdFn(ctx, "systemctl", "disable", fmt.Sprintf("jabali-fpm@%s.service", p.Username))

	// JAB-263 phase D: cut any FTPS worker the pam_exec hook placed in this
	// tenant's ftp-sessions leaf and remove the leaf, BEFORE stopping the slice.
	// A populated leaf keeps the slice cgroup non-empty, so the slice stop +
	// the caller's userdel would otherwise strand live processes and leave the
	// slice un-removable.
	killTenantFtpSessionLeaf(ctx, runCmdFn, p.Username)

	// Stop slice unit (ignore "not loaded" errors)
	_, _, _ = runCmdFn(ctx, "systemctl", "stop", fmt.Sprintf("jabali-user-%s.slice", p.Username))

	// Terminate the user's systemd --user session and all remaining processes
	// (sd-pam, any shells). Without this userdel exits 8 ("user currently
	// logged in") even after the FPM service is stopped.
	_, _, _ = runCmdFn(ctx, "loginctl", "terminate-user", p.Username)

	// Remove unit files
	sliceUnitPath := filepath.Join(root, fmt.Sprintf("jabali-user-%s.slice", p.Username))
	fpmDropinDir := filepath.Join(root, fmt.Sprintf("jabali-fpm@%s.service.d", p.Username))
	fpmDropinPath := filepath.Join(fpmDropinDir, "slice.conf")

	sliceRemoved := removeFile(sliceUnitPath)
	fpmDropinRemoved := removeFile(fpmDropinPath)
	if fpmDropinRemoved {
		removeEmptyDir(fpmDropinDir)
	}

	// Remove login dropin if we resolved uid
	loginDropinRemoved := false
	if uid > 0 {
		loginDropinDir := filepath.Join(root, fmt.Sprintf("user@%d.service.d", uid))
		loginDropinPath := filepath.Join(loginDropinDir, "jabali.conf")
		loginDropinRemoved = removeFile(loginDropinPath)
		if loginDropinRemoved {
			removeEmptyDir(loginDropinDir)
		}
	}

	// Reload systemd
	testMutex.Lock()
	runCmdReload := runCmd
	testMutex.Unlock()
	_, _, _ = runCmdReload(ctx, "systemctl", "daemon-reload")

	// Determine if anything was removed
	removed := sliceRemoved || fpmDropinRemoved || loginDropinRemoved
	alreadyAbsent := !sliceRemoved && !fpmDropinRemoved && !loginDropinRemoved

	return &userSliceRemoveResponse{
		Username:      p.Username,
		Removed:       removed,
		AlreadyAbsent: alreadyAbsent,
	}, nil
}

// killTenantFtpSessionLeaf kills every process in ONE tenant's ftp-sessions leaf
// cgroup (JAB-263 phase D) and removes the leaf. Best-effort: it asks systemd
// for the slice's real cgroup path (so a dashed username still resolves), then
// uses cgroup.kill. A failure just leaves the leaf, which the reconciler /
// next teardown retries.
func killTenantFtpSessionLeaf(ctx context.Context, runCmdFn func(context.Context, string, ...string) ([]byte, []byte, error), username string) {
	out, _, err := runCmdFn(ctx, "systemctl", "show",
		fmt.Sprintf("jabali-user-%s.slice", username), "-p", "ControlGroup", "--value")
	if err != nil {
		return
	}
	cg := strings.TrimSpace(string(out))
	if cg == "" || cg == "/" {
		return
	}
	leaf := filepath.Join("/sys/fs/cgroup", cg, ftpSessionLeafName)
	_ = os.WriteFile(filepath.Join(leaf, "cgroup.kill"), []byte("1"), 0o644)
	_ = os.Remove(leaf)
}

// removeFile attempts to remove a file, returning true if it existed and was removed.
func removeFile(path string) bool {
	err := os.Remove(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	// If there was an error other than ENOENT, we still return false
	// and let the caller handle the state.
	return false
}

// removeEmptyDir attempts to remove a directory if it's empty.
func removeEmptyDir(path string) {
	os.Remove(path)
}

func init() {
	Default.Register("user.slice.remove", userSliceRemoveHandler)
}
