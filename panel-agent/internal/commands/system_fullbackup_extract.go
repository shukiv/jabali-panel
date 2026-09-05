package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
)

// system_fullbackup.extract — GH #1408 slice 2 (create-from-manifest for the
// full-server container). Instead of the agent looping restoreAccountFromTar
// internally (system.fullbackup.restore_uploaded, which discards each user's
// metadata + can't create a missing account), the PANEL drives the loop: it
// extracts the container once, then per selected user runs the SAME per-account
// path it uses for a single upload — create-if-missing, backup.restore_from_tar,
// then the panel-side metadata rebuild. This restores full panel state (domains
// / databases / mailboxes) on a fresh box, works on containers packed by any
// prior version (identity is read from each inner tar), and reports per-user
// progress instead of one all-or-nothing socket call.
//
// This verb only EXTRACTS (safe plain-tar) to a stage under the uploads root
// and returns the inner-tar paths; it applies nothing. The stage persists for
// the panel loop and is removed by system.fullbackup.cleanup_stage.

const fullRestoreStagePrefix = "fullrestore-"

// fullRestoreStageTTL bounds how long an abandoned extraction lingers (a panel
// crash mid-loop). evictStaleFullRestoreStages reaps older ones on each extract.
const fullRestoreStageTTL = 24 * time.Hour

type systemFullbackupExtractParams struct {
	TarPath string `json:"tar_path"`
}

type extractedContainerUser struct {
	Username  string `json:"username"`
	InnerPath string `json:"inner_path"` // under restoreUploadsRoot → valid backup.restore_from_tar input
}

type systemFullbackupExtractResult struct {
	Stage       string                   `json:"stage"`
	RunID       string                   `json:"run_id"`
	Users       []extractedContainerUser `json:"users"`
	SystemInner string                   `json:"system_inner,omitempty"` // slice 3 reuse
}

// evictStaleFullRestoreStages removes fullrestore-* staging dirs older than the
// TTL — self-heals a panel that crashed between extract and cleanup.
func evictStaleFullRestoreStages() {
	matches, _ := filepath.Glob(filepath.Join(restoreUploadsRoot, fullRestoreStagePrefix+"*"))
	cutoff := time.Now().Add(-fullRestoreStageTTL)
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() && fi.ModTime().Before(cutoff) {
			_ = os.RemoveAll(m)
		}
	}
}

func systemFullbackupExtractUploadedHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p systemFullbackupExtractParams
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
	evictStaleFullRestoreStages()

	stage := filepath.Join(restoreUploadsRoot, fullRestoreStagePrefix+randomULID())
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o750); err != nil {
		return nil, bkInternal("mkdir stage", err)
	}
	// NO defer RemoveAll: the stage persists for the panel's per-user loop;
	// system.fullbackup.cleanup_stage removes it when the loop finishes.
	if _, err := safeExtractPlainTar(clean, stage); err != nil {
		_ = os.RemoveAll(stage)
		return nil, bkInvalidArg("container rejected: " + err.Error())
	}
	mb, err := readFileFromPlainTar(clean, "manifest.json")
	if err != nil {
		_ = os.RemoveAll(stage)
		return nil, bkInvalidArg("container has no manifest.json")
	}
	var man fullContainerManifest
	if json.Unmarshal(mb, &man) != nil {
		_ = os.RemoveAll(stage)
		return nil, bkInvalidArg("container manifest parse failed")
	}

	out := systemFullbackupExtractResult{Stage: stage, RunID: man.RunID}
	for _, e := range man.Entries {
		if e.Label == "system" {
			sp := filepath.Join(stage, "system.tar.zst")
			if fi, serr := os.Stat(sp); serr == nil && fi.Mode().IsRegular() {
				out.SystemInner = sp
			}
			continue
		}
		if e.Username == "" {
			continue
		}
		inner := filepath.Join(stage, "users", e.Username+".tar.zst")
		if fi, serr := os.Stat(inner); serr != nil || !fi.Mode().IsRegular() {
			continue // inner archive missing from the container — skip; the loop reports it
		}
		out.Users = append(out.Users, extractedContainerUser{Username: e.Username, InnerPath: inner})
	}
	return out, nil
}

type systemFullbackupCleanupParams struct {
	Stage string `json:"stage"`
}

// systemFullbackupCleanupStageHandler removes one extraction stage. It is a
// root-privileged RemoveAll, so the target is confined HARD: it must be a clean
// path whose parent is EXACTLY restoreUploadsRoot and whose basename carries the
// fullrestore- prefix. Anything else is refused — this can never be steered at
// an arbitrary directory.
func systemFullbackupCleanupStageHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p systemFullbackupCleanupParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg(fmt.Sprintf("invalid params: %v", err))
	}
	clean := filepath.Clean(p.Stage)
	if clean != p.Stage ||
		filepath.Dir(clean) != restoreUploadsRoot ||
		!strings.HasPrefix(filepath.Base(clean), fullRestoreStagePrefix) {
		return nil, bkInvalidArg("refusing to clean a path outside the restore staging root")
	}
	if err := os.RemoveAll(clean); err != nil {
		return nil, bkInternal("cleanup stage", err)
	}
	return map[string]any{"cleaned": clean}, nil
}

func init() {
	Default.Register("system.fullbackup.extract_uploaded", systemFullbackupExtractUploadedHandler)
	Default.Register("system.fullbackup.cleanup_stage", systemFullbackupCleanupStageHandler)
}
