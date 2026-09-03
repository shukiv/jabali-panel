package commands

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
)

// system_fullbackup.restore — GH #1408 phase 2. Restore from an UPLOADED full-
// server container (produced by system.fullbackup.pack): a PLAIN tar of
// manifest.json + system.tar.zst + users/<username>.tar.zst, where each inner
// tar is byte-identical to a per-account backup. So restore = extract the outer
// container (safe plain-tar extractor) then feed each selected inner tar through
// the SAME restoreAccountFromTar core the single-upload restore uses.
//
// The container itself is untrusted (safe extractor), and each inner tar is
// untrusted again (restoreAccountFromTar's hardened zstd extractor) — double
// safe. System restore is deliberately NOT applied here: it changes server
// config and stays a manual/CLI, step-up-worthy operation in v1.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func randomULID() string {
	rnd := make([]byte, 26)
	_, _ = rand.Read(rnd)
	b := make([]byte, 26)
	for i := range b {
		b[i] = crockford[int(rnd[i])%32]
	}
	return string(b)
}

type fullContainerManifest struct {
	RunID   string `json:"run_id"`
	Schema  int    `json:"schema"`
	Entries []struct {
		Username string `json:"username,omitempty"`
		Label    string `json:"label"`
		JobID    string `json:"job_id"`
	} `json:"entries"`
}

func fullContainerPathClean(tarPath string) (string, error) {
	clean := filepath.Clean(tarPath)
	if clean != tarPath || !strings.HasPrefix(clean, restoreUploadsRoot+"/") {
		return "", bkInvalidArg("tar_path must be a clean path under " + restoreUploadsRoot)
	}
	if fi, err := os.Lstat(clean); err != nil || !fi.Mode().IsRegular() {
		return "", bkInvalidArg("tar_path is not a regular file")
	}
	return clean, nil
}

type systemFullbackupInspectParams struct {
	TarPath string `json:"tar_path"`
}

// system.fullbackup.inspect_uploaded reads the container's manifest (no full
// extraction) so the UI can offer System + which users to restore.
func systemFullbackupInspectUploadedHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p systemFullbackupInspectParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg(fmt.Sprintf("invalid params: %v", err))
	}
	clean, aerr := fullContainerPathClean(p.TarPath)
	if aerr != nil {
		return nil, aerr
	}
	mb, err := readFileFromPlainTar(clean, "manifest.json")
	if err != nil {
		return nil, bkInvalidArg("not a full-server backup container (no manifest.json): " + err.Error())
	}
	var man fullContainerManifest
	if json.Unmarshal(mb, &man) != nil {
		return nil, bkInvalidArg("container manifest parse failed")
	}
	users := make([]string, 0, len(man.Entries))
	hasSystem := false
	for _, e := range man.Entries {
		if e.Label == "system" {
			hasSystem = true
			continue
		}
		if e.Username != "" {
			users = append(users, e.Username)
		}
	}
	return map[string]any{"run_id": man.RunID, "users": users, "has_system": hasSystem}, nil
}

type systemFullbackupRestoreParams struct {
	TarPath   string   `json:"tar_path"`
	Usernames []string `json:"usernames"`      // which users to restore
	System    bool     `json:"include_system"` // v1: acknowledged but not applied
}

type fullRestoreUserResult struct {
	Username string   `json:"username"`
	Applied  []string `json:"applied,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type systemFullbackupRestoreResult struct {
	Users      []fullRestoreUserResult `json:"users"`
	Skipped    []string                `json:"skipped,omitempty"`
	SystemNote string                  `json:"system_note,omitempty"`
}

// system.fullbackup.restore_uploaded extracts the container and restores each
// selected user's inner tar via restoreAccountFromTar.
func systemFullbackupRestoreUploadedHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p systemFullbackupRestoreParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg(fmt.Sprintf("invalid params: %v", err))
	}
	clean, aerr := fullContainerPathClean(p.TarPath)
	if aerr != nil {
		return nil, aerr
	}
	if err := hostreserve.CheckReserve("/var/lib/jabali-backups", 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: "restore staging is under the host disk reserve: " + err.Error()}
	}

	// Extract the container UNDER the uploads root so the inner user tars are
	// valid inputs to restoreAccountFromTar (which requires that prefix).
	stage := filepath.Join(restoreUploadsRoot, "fullrestore-"+randomULID())
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o750); err != nil {
		return nil, bkInternal("mkdir stage", err)
	}
	defer os.RemoveAll(stage)
	if _, err := safeExtractPlainTar(clean, stage); err != nil {
		return nil, bkInvalidArg("container rejected: " + err.Error())
	}

	mb, err := readFileFromPlainTar(clean, "manifest.json")
	if err != nil {
		return nil, bkInvalidArg("container has no manifest.json")
	}
	var man fullContainerManifest
	if json.Unmarshal(mb, &man) != nil {
		return nil, bkInvalidArg("container manifest parse failed")
	}

	want := map[string]bool{}
	for _, u := range p.Usernames {
		want[u] = true
	}

	out := systemFullbackupRestoreResult{}
	if p.System {
		out.SystemNote = "system restore is not applied from the panel — run the system_restore CLI on the box"
	}
	for _, e := range man.Entries {
		if e.Label == "system" || e.Username == "" {
			continue
		}
		if len(want) > 0 && !want[e.Username] {
			out.Skipped = append(out.Skipped, e.Username)
			continue
		}
		inner := filepath.Join(stage, "users", e.Username+".tar.zst")
		if fi, serr := os.Stat(inner); serr != nil || !fi.Mode().IsRegular() {
			out.Users = append(out.Users, fullRestoreUserResult{Username: e.Username, Error: "inner archive missing from container"})
			continue
		}
		res, rerr := restoreAccountFromTar(ctx, randomULID(), inner, e.Username, nil, true, restoreEnforcement{})
		if rerr != nil {
			msg := rerr.Error()
			if ae, ok := rerr.(*agentwire.AgentError); ok && ae.Message != "" {
				msg = ae.Message
			}
			out.Users = append(out.Users, fullRestoreUserResult{Username: e.Username, Error: msg})
			continue
		}
		out.Users = append(out.Users, fullRestoreUserResult{Username: e.Username, Applied: res.Applied, Warnings: res.Warnings})
	}
	return out, nil
}

func init() {
	Default.Register("system.fullbackup.inspect_uploaded", systemFullbackupInspectUploadedHandler)
	Default.Register("system.fullbackup.restore_uploaded", systemFullbackupRestoreUploadedHandler)
}
