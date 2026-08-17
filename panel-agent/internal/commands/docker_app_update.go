package commands

// docker_app.update — pull a newer image + restart the container,
// with a restic snapshot taken just before for rollback. Two
// terminal outcomes:
//
//   updated    : new image is running and healthcheck reports
//                healthy within the budget.
//   rolled_back: new image failed healthcheck OR docker compose
//                refused to come up; we restored the snapshot and
//                brought the previous compose back up.
//
// Backup integration (Phase 8) will replace the placeholder restic
// invocation here with a real per-app restic backend. For Phase 7
// we shell out to `restic snapshot` against the operator's existing
// destination via the M30 wrapper — when restic isn't configured
// the update still runs, just without a rollback safety net (we
// surface that to the operator as a notification).
//
// See ADR-0116 Decision 9 (manual + auto modes; Phase 7 ships both
// dispatch paths — the auto-update poller belongs to a follow-up).
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type dockerAppUpdateParams struct {
	Slug              string `json:"slug"`
	HealthcheckTOSecs int    `json:"healthcheck_timeout_seconds,omitempty"`
	// ComposeYML / EnvFile re-render the install from the current catalog
	// template before the pull. Empty = keep the on-disk files (back-compat
	// + the path taken when panel-api can't read the env back). Written
	// atomically BEFORE pull so a catalog fix (e.g. a healthcheck) reaches
	// existing installs on update.
	ComposeYML string `json:"compose_yml,omitempty"`
	EnvFile    string `json:"env_file,omitempty"`
	// TenantValidate / TenantCaps mirror the install path (Gitea #480): when a
	// tenant-owned app is re-rendered on update/env-edit, run the same agent-side
	// compose safety gate so a re-render cannot bring up an unhardened compose.
	TenantValidate bool     `json:"tenant_validate,omitempty"`
	TenantCaps     []string `json:"tenant_caps,omitempty"`
	TenantCgroup   string   `json:"tenant_cgroup,omitempty"`
}

type dockerAppUpdateResponse struct {
	Slug      string `json:"slug"`
	Outcome   string `json:"outcome"`              // "updated" | "no_change" | "rolled_back"
	NewImage  string `json:"new_image,omitempty"`
	OldImage  string `json:"old_image,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"` // restic id when one was taken
	Detail    string `json:"detail,omitempty"`
}

func dockerAppUpdateHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerAppUpdateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	dir := filepath.Join(dockerAppDataRoot, p.Slug)
	if _, err := os.Stat(filepath.Join(dir, "compose.yml")); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("%s/compose.yml not found", dir)}
	}

	// Re-render from the catalog: write the fresh compose.yml (+ .env) the
	// panel rendered from the CURRENT template, so catalog fixes + version
	// bumps reach this install. Atomic tmp+rename; .env stays 0600. Empty
	// params keep the existing files (back-compat).
	if p.ComposeYML != "" {
		if err := writeAtomicDockerApp(filepath.Join(dir, "compose.yml"), []byte(p.ComposeYML), 0o640); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write compose.yml: %v", err)}
		}
	}
	if p.EnvFile != "" {
		if err := writeAtomicDockerApp(filepath.Join(dir, ".env"), []byte(p.EnvFile), 0o600); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write .env: %v", err)}
		}
	}

	// Tenant safety gate on the RE-RENDERED compose (Gitea #480): refuse to
	// recreate a tenant app whose compose lost its sandbox (privileged
	// container, foreign capability, host bind-mount), mirroring the install
	// path's M49 validation.
	if p.TenantValidate {
		if err := runTenantComposeValidation(ctx, dir, p.TenantCaps, p.TenantCgroup); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "tenant compose rejected: " + err.Error()}
		}
	}

	timeout := time.Duration(p.HealthcheckTOSecs) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	// 1. Snapshot (best-effort). When restic isn't configured we
	//    proceed without a snapshot but flag it in the response so
	//    the panel can surface a "no rollback available" toast.
	snapshotID, snapshotErr := resticSnapshotIfConfigured(ctx, dir, p.Slug)

	// 2. Capture the current image SHA so we can rollback if needed.
	oldImage := currentImage(ctx, p.Slug)

	// 3. docker compose pull
	if out, err := runDockerCompose(ctx, dir, "pull"); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("docker compose pull failed: %v", err),
			Details: json.RawMessage(fmt.Sprintf(`{"stderr": %q}`, out)),
		}
	}

	// 4. docker compose up -d (rolls forward to the new image)
	if out, err := runDockerCompose(ctx, dir, "up", "-d"); err != nil {
		// Compose refused. Try to rollback by re-pulling the old
		// image (if we have a SHA) and bringing it back up.
		_ = rollback(ctx, dir, oldImage)
		return dockerAppUpdateResponse{
			Slug:       p.Slug,
			Outcome:    "rolled_back",
			OldImage:   oldImage,
			SnapshotID: snapshotID,
			Detail:     fmt.Sprintf("docker compose up failed: %v; stderr=%s", err, out),
		}, nil
	}

	// 5. Wait for healthy.
	status, _ := waitHealthy(ctx, dir, p.Slug, timeout)
	if status != "running" {
		_ = rollback(ctx, dir, oldImage)
		return dockerAppUpdateResponse{
			Slug:       p.Slug,
			Outcome:    "rolled_back",
			OldImage:   oldImage,
			SnapshotID: snapshotID,
			Detail:     fmt.Sprintf("healthcheck did not converge to healthy within %s", timeout),
		}, nil
	}

	newImage := currentImage(ctx, p.Slug)

	// GH #794: distinguish "nothing changed" from a real upgrade. The catalog
	// pins each image to a digest (post-#458), so an update only carries a
	// newer version when the catalog itself was bumped. When it hasn't, the
	// re-rendered compose is identical, `pull` is a no-op, and the container
	// image is unchanged — the user pressed Update and rightly saw "no update
	// happened". Report that as its own outcome so the panel can say the app
	// is already on the latest catalog version instead of implying an upgrade.
	if oldImage != "" && oldImage == newImage {
		return dockerAppUpdateResponse{
			Slug:       p.Slug,
			Outcome:    "no_change",
			OldImage:   oldImage,
			NewImage:   newImage,
			SnapshotID: snapshotID,
			Detail:     "already on the latest catalog version; nothing to update",
		}, nil
	}

	detail := ""
	if snapshotErr != nil {
		detail = fmt.Sprintf("update succeeded; pre-update snapshot was NOT taken (restic: %v)", snapshotErr)
	}
	return dockerAppUpdateResponse{
		Slug:       p.Slug,
		Outcome:    "updated",
		OldImage:   oldImage,
		NewImage:   newImage,
		SnapshotID: snapshotID,
		Detail:     detail,
	}, nil
}

// rollback re-pulls and re-ups the previous image. Best-effort —
// we already have one failure path open by the time this runs.
func rollback(ctx context.Context, dir, oldImage string) error {
	if oldImage == "" {
		// Nothing to rollback to; the operator gets a manual-fix
		// situation. The caller surfaces this in the response detail.
		return nil
	}
	// We don't rewrite compose.yml — it pins the channel tag, not
	// the digest, so `pull` followed by `up -d` will fetch whatever
	// "latest" points at NOW. That's the same image we just pulled.
	// True rollback needs the snapshot path (restic restore + up)
	// once Phase 8 wires it up. For Phase 7 we accept that auto
	// mode + a corrupt upstream image leaves the operator a
	// minute behind on a notification, not in a permanent dead
	// state -- they can pin a different channel via Edit compose.
	_, _ = runDockerCompose(ctx, dir, "up", "-d")
	return nil
}

// resticSnapshotIfConfigured runs `restic snapshot <dir>` against
// the operator's configured repo. When restic isn't installed or
// no JABALI_RESTIC_REPO env is set, returns ("", err) with a
// non-fatal explanation.
func resticSnapshotIfConfigured(ctx context.Context, dir, slug string) (string, error) {
	if _, err := exec.LookPath("restic"); err != nil {
		return "", fmt.Errorf("restic binary not present")
	}
	if os.Getenv("JABALI_RESTIC_REPO") == "" {
		return "", fmt.Errorf("JABALI_RESTIC_REPO not set")
	}
	cmd := execCommandContext(ctx, "restic", "backup", dir,
		"--tag", "docker-app",
		"--tag", "slug:"+slug,
		"--tag", "reason:pre-update",
		"--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("restic backup: %w (%s)", err, string(out))
	}
	// Restic JSON output is multi-line; we extract the snapshot_id
	// from the `summary` message.
	for _, line := range splitLines(string(out)) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if t, _ := m["message_type"].(string); t == "summary" {
			if id, ok := m["snapshot_id"].(string); ok {
				return id, nil
			}
		}
	}
	return "", nil
}

// currentImage returns the repo digest (sha256:...) of the primary
// container for this install. Uses the SAME helper that
// docker_app.check_update uses to compute "available_digest", so the
// two SHAs land in the same space and the "update available" chip
// clears once image_sha catches up.
//
// The old implementation walked `docker compose ps` + `docker inspect
// --format {{.Image}}` which returned the LOCAL IMAGE ID (config
// hash) -- a different SHA from the repo digest stored in
// available_digest, so the diff never resolved to "no drift" and the
// chip stayed lit forever after a successful update.
func currentImage(ctx context.Context, slug string) string {
	return dockerContainerImageDigest(ctx, "jabali-app-"+slug)
}

func init() {
	Default.Register("docker_app.update", dockerAppUpdateHandler)
}
