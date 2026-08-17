// docker_app.backup / .list_backups / .restore — restic-backed
// per-app snapshot REST surface for M48 Phase 8.
//
// Phase 8.1 (this file): standalone restic against
// JABALI_RESTIC_REPO + JABALI_RESTIC_PASSWORD env vars (set on the
// jabali-agent.service systemd unit). Operator points these at their
// existing backup destination. Phase 8.2 will wire the
// backup_destinations table for per-destination selection at install
// time -- separate PR to keep the diff reviewable.
//
// Snapshot taxonomy uses restic --tag so a per-app snapshot is
// trivially queryable later:
//
//	docker-app, slug:<slug>, reason:manual | reason:pre-update,
//	panel-managed
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type dockerAppBackupParams struct {
	Slug          string `json:"slug"`
	Reason        string `json:"reason,omitempty"`
	DestinationID string `json:"destination_id,omitempty"`
}

type dockerAppBackupResponse struct {
	Slug       string `json:"slug"`
	SnapshotID string `json:"snapshot_id"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

func dockerAppBackupHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerAppBackupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	if p.Reason == "" {
		p.Reason = "manual"
	}
	dir := filepath.Join(dockerAppDataRoot, p.Slug)
	if _, err := os.Stat(dir); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("%s not found", dir)}
	}
	env, eerr := dockerAppResticEnvFor(p.DestinationID)
	if eerr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: eerr.Error()}
	}

	cmd := execCommandContext(ctx, "restic", "backup", dir,
		"--tag", "docker-app",
		"--tag", "panel-managed",
		"--tag", "slug:"+p.Slug,
		"--tag", "reason:"+p.Reason,
		"--json")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("restic backup failed: %v", err),
			Details: json.RawMessage(fmt.Sprintf(`{"stderr": %q}`, out)),
		}
	}

	resp := dockerAppBackupResponse{Slug: p.Slug}
	for _, line := range splitLines(string(out)) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if t, _ := m["message_type"].(string); t == "summary" {
			if id, ok := m["snapshot_id"].(string); ok {
				resp.SnapshotID = id
			}
			if sz, ok := m["data_added"].(float64); ok {
				resp.SizeBytes = int64(sz)
			}
		}
	}
	return resp, nil
}

type dockerAppListBackupsParams struct {
	Slug          string `json:"slug"`
	DestinationID string `json:"destination_id,omitempty"`
}

type dockerAppBackupRow struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname,omitempty"`
	Tags     []string  `json:"tags,omitempty"`
}

type dockerAppListBackupsResponse struct {
	Slug    string               `json:"slug"`
	Backups []dockerAppBackupRow `json:"backups"`
}

func dockerAppListBackupsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerAppListBackupsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	env, eerr := dockerAppResticEnvFor(p.DestinationID)
	if eerr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: eerr.Error()}
	}

	cmd := execCommandContext(ctx, "restic", "snapshots",
		"--tag", "docker-app",
		"--tag", "slug:"+p.Slug,
		"--json")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("restic snapshots failed: %v", err),
			Details: json.RawMessage(fmt.Sprintf(`{"stderr": %q}`, out)),
		}
	}

	resp := dockerAppListBackupsResponse{Slug: p.Slug, Backups: []dockerAppBackupRow{}}
	var arr []map[string]any
	if err := json.Unmarshal(out, &arr); err != nil {
		// Empty repo or older restic: tolerate.
		return resp, nil
	}
	for _, m := range arr {
		row := dockerAppBackupRow{}
		if id, ok := m["short_id"].(string); ok {
			row.ID = id
		}
		if id, ok := m["id"].(string); ok && row.ID == "" {
			row.ID = id
		}
		if ts, ok := m["time"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				row.Time = t
			}
		}
		if h, ok := m["hostname"].(string); ok {
			row.Hostname = h
		}
		if tags, ok := m["tags"].([]any); ok {
			for _, t := range tags {
				if s, ok := t.(string); ok {
					row.Tags = append(row.Tags, s)
				}
			}
		}
		resp.Backups = append(resp.Backups, row)
	}
	return resp, nil
}

type dockerAppRestoreParams struct {
	Slug          string `json:"slug"`
	SnapshotID    string `json:"snapshot_id"`
	DestinationID string `json:"destination_id,omitempty"`
	// TenantValidate / TenantCaps (Gitea #509): when the app is tenant-owned,
	// re-run the tenant compose safety gate on the RESTORED compose before up.
	TenantValidate bool     `json:"tenant_validate,omitempty"`
	TenantCaps     []string `json:"tenant_caps,omitempty"`
	TenantCgroup   string   `json:"tenant_cgroup,omitempty"`
}

type dockerAppRestoreResponse struct {
	Slug       string `json:"slug"`
	SnapshotID string `json:"snapshot_id"`
	Detail     string `json:"detail,omitempty"`
}

// dockerAppRestoreHandler restores the per-app data tree from a
// restic snapshot. Sequence:
//
//  1. docker compose down  — container must not be writing volumes
//     mid-restore.
//  2. restic restore <id> --target /var/lib/jabali/docker-apps/<slug>
//     with --include trimmed to the slug subtree.
//  3. docker compose up -d  — bring the app back up on the restored
//     state.
//
// On failure we leave the operator with whatever state restic landed
// in; the runbook covers manual recovery.
func dockerAppRestoreHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerAppRestoreParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	if p.SnapshotID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "snapshot_id is required"}
	}
	dir := filepath.Join(dockerAppDataRoot, p.Slug)
	env, eerr := dockerAppResticEnvFor(p.DestinationID)
	if eerr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: eerr.Error()}
	}

	if _, err := runDockerCompose(ctx, dir, "down"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("docker compose down: %v", err)}
	}

	rc := execCommandContext(ctx, "restic", "restore", p.SnapshotID,
		"--target", "/",
		"--include", dir,
		"--json")
	rc.Env = env
	if out, err := rc.CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("restic restore failed: %v", err),
			Details: json.RawMessage(fmt.Sprintf(`{"stderr": %q}`, out)),
		}
	}

	// Tenant-owned restore (Gitea #509): the restored snapshot may carry an
	// old/unsafe compose.yml. Re-run the tenant safety gate before bringing it
	// up; fail closed (leave it down) rather than start an unhardened compose.
	if p.TenantValidate {
		if err := runTenantComposeValidation(ctx, dir, p.TenantCaps, p.TenantCgroup); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: fmt.Sprintf("restored compose failed tenant validation: %v", err)}
		}
	}
	if out, err := runDockerCompose(ctx, dir, "up", "-d"); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: composeFailMessage("up -d (after restore)", out, err),
			Details: json.RawMessage(fmt.Sprintf(`{"stderr": %q}`, out)),
		}
	}
	return dockerAppRestoreResponse{Slug: p.Slug, SnapshotID: p.SnapshotID, Detail: "compose down -> restore -> compose up -d"}, nil
}

// requireResticBinary enforces the absolute baseline: restic exists.
func requireResticBinary() error {
	if _, err := exec.LookPath("restic"); err != nil {
		return fmt.Errorf("restic binary not present")
	}
	return nil
}

// requireResticEnv enforces the Phase 8.1 fallback: JABALI_RESTIC_REPO
// + JABALI_RESTIC_PASSWORD set on jabali-agent.service. Called only
// when no destination_id was supplied.
func requireResticEnv() error {
	if err := requireResticBinary(); err != nil {
		return err
	}
	if os.Getenv("JABALI_RESTIC_REPO") == "" {
		return fmt.Errorf("JABALI_RESTIC_REPO not set on jabali-agent.service")
	}
	if os.Getenv("JABALI_RESTIC_PASSWORD") == "" {
		return fmt.Errorf("JABALI_RESTIC_PASSWORD not set on jabali-agent.service")
	}
	return nil
}

// resticRemoteEnvDir is where M30.1 wrote per-destination env files.
// Each file is shell-style KEY=VALUE; lines like RESTIC_REPOSITORY=...
// + RESTIC_PASSWORD=... + cred-helper exports (AWS_*, B2_*, etc.) are
// pre-rendered by the panel at destination-create time.
const resticRemoteEnvDir = "/etc/jabali-panel/restic-remotes"

// dockerAppResticEnvFor returns the process env restic should run
// under. When destID is set, parses /etc/jabali-panel/restic-remotes/
// <id>.env and layers its KEY=VALUE pairs on top of os.Environ.
// When empty, falls back to the JABALI_RESTIC_REPO + JABALI_RESTIC_
// PASSWORD env vars (Phase 8.1 path).
func dockerAppResticEnvFor(destID string) ([]string, error) {
	env := os.Environ()
	if destID == "" {
		if err := requireResticEnv(); err != nil {
			return nil, err
		}
		env = append(env,
			"RESTIC_REPOSITORY="+os.Getenv("JABALI_RESTIC_REPO"),
			"RESTIC_PASSWORD="+os.Getenv("JABALI_RESTIC_PASSWORD"),
		)
		return env, nil
	}
	// destination_id path. Validate shape (ULID charset only) so we
	// can't be tricked into reading an arbitrary file.
	if !ulidLikeRE.MatchString(destID) {
		return nil, fmt.Errorf("invalid destination_id %q", destID)
	}
	if err := requireResticBinary(); err != nil {
		return nil, err
	}
	path := filepath.Join(resticRemoteEnvDir, destID+".env")
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read destination env %s: %w", path, err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		env = append(env, line)
	}
	return env, nil
}

var ulidLikeRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// quiet build-time use to keep `strings` imported across refactors;
// the package collectively uses it, but this file may temporarily
// not.
var _ = strings.TrimSpace

func init() {
	Default.Register("docker_app.backup", dockerAppBackupHandler)
	Default.Register("docker_app.list_backups", dockerAppListBackupsHandler)
	Default.Register("docker_app.restore", dockerAppRestoreHandler)
}
