// reconcileDockerApps converges docker_apps rows with the docker
// daemon's view of the world (via the agent's docker_app.* verbs).
//
// State machine (see ADR-0116 + plans/m48-docker-app-marketplace.md):
//
//	pending      -> dispatch docker_app.install; on success move to
//	                `running` (or `unhealthy` if healthchecks failed
//	                within the install verb's wait_healthy budget)
//	installing   -> in-flight install; status-poll to find out
//	                whether it landed in `running` / `failed`
//	running      -> status-poll; if the agent says not-running,
//	                move to `failed` (a crashed-after-install case)
//	stopped      -> no-op; operator owns the next move
//	failed       -> no-op; operator clicks "Retry" to flip back to
//	                pending
//	updating, rolling_back, deleted -> not handled in Phase 3
//	                (Phase 7 ships update/rollback; deletion is
//	                synchronous in the REST handler).
//
// The reconciler only DISPATCHES work; it never blocks waiting for
// the docker daemon. The agent verbs have their own deadlines.
package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// dockerAppSizeInterval gates the du(1) data-size refresh so it runs on a
// slow cadence rather than every status tick (which is otherwise cheap).
const dockerAppSizeInterval = 15 * time.Minute

// reconcileDockerApps is called once per reconciler tick (the same
// cadence as domain reconciliation). The signature matches sibling
// tick functions; the docker-app repo is optional so a deploy without
// M48 wired still boots cleanly.
func (r *Reconciler) reconcileDockerApps(ctx context.Context) {
	if r.dockerApps == nil || r.agent == nil {
		return
	}

	// Keep the tenant-Docker gate honest (Gitea #512): the agent verifies the
	// LIVE daemon still has userns-remap and removes the tenant flag if not, so
	// panel-api's flag-stat gate fails closed instead of admitting tenants to a
	// non-remapped daemon. Best-effort, once per tick; no-op when the flag is
	// absent. (#506 covers install/update; this covers out-of-band drift.)
	func() {
		hctx, hcancel := context.WithTimeout(ctx, 15*time.Second)
		defer hcancel()
		raw, herr := r.agent.Call(hctx, "docker.tenant_health", map[string]any{})
		if herr != nil {
			return
		}
		var h struct {
			FlagRemoved bool `json:"flag_removed"`
		}
		if json.Unmarshal(raw, &h) == nil && h.FlagRemoved {
			r.log.Warn("dockerapp: removed tenant flag — live daemon lacks userns-remap (Gitea #512)")
		}
	}()

	apps, err := r.dockerApps.ListAll(ctx)
	if err != nil {
		r.log.Warn("dockerapp: listAll failed", "err", err)
		return
	}
	// Enforce per-package Docker entitlement (Gitea #521): a package downgrade
	// or a user moved to a smaller package can leave a tenant running more apps
	// than allowed. Stop the newest excess (keep the oldest N), fail-safe — we
	// stop, never delete, so raising the limit lets the tenant start them again.
	r.enforceDockerEntitlements(ctx, apps)
	for _, app := range apps {
		if app == nil {
			continue
		}
		r.reconcileOneDockerApp(ctx, app)
	}
}

// enforceDockerEntitlements stops a tenant's over-the-limit Docker apps. Groups
// tenant-owned apps by user, keeps the oldest <limit> and stops the newer
// excess that is still running/installing. Admin/server apps (UserID nil) are
// exempt.
func (r *Reconciler) enforceDockerEntitlements(ctx context.Context, apps []*models.DockerApp) {
	if r.users == nil || r.packages == nil {
		return
	}
	byUser := map[string][]*models.DockerApp{}
	for _, a := range apps {
		if a == nil || a.UserID == nil {
			continue
		}
		byUser[*a.UserID] = append(byUser[*a.UserID], a)
	}
	for uid, list := range byUser {
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
		// --- count cap (Gitea #521): keep oldest <limit>, stop newer excess ---
		limit := r.effectiveDockerLimit(ctx, uid)
		over := len(list) - limit
		for i := 0; i < over; i++ {
			a := list[i]
			if a.Status == models.DockerAppStatusRunning || a.Status == models.DockerAppStatusInstalling {
				r.stopOverEntitlement(ctx, a)
			}
		}
		// --- disk cap (Gitea #489): a tenant's total docker footprint must not
		// exceed the package disk quota. Stopping doesn't reclaim disk (data
		// persists on the bind mount) but HALTS further growth — the tenant
		// frees space by deleting an app, then can restart. install-time gate
		// (b6ad0317) already blocks new installs over quota; this is the runtime
		// half. Skip apps already stopped above by the count cap. ---
		quotaBytes := r.effectiveDockerDiskQuotaBytes(ctx, uid)
		if quotaBytes <= 0 {
			continue
		}
		var used int64
		for _, a := range list {
			used += a.DataBytes
		}
		if used <= quotaBytes {
			continue
		}
		for _, a := range list {
			if a.Status == models.DockerAppStatusRunning || a.Status == models.DockerAppStatusInstalling {
				r.stopOverDiskQuota(ctx, a)
			}
		}

		// --- allowlist cap (Gitea #521 follow-up): if the package pins a
		// DockerAppSlugs allowlist and an app's catalog slug is no longer on it
		// (admin removed it), stop the running instance. Empty allowlist = no
		// restriction. ---
		allow := r.dockerAppAllowlist(ctx, uid)
		if allow != nil {
			for _, a := range list {
				if allow[a.Slug] {
					continue
				}
				if a.Status == models.DockerAppStatusRunning || a.Status == models.DockerAppStatusInstalling {
					r.stopDisallowedApp(ctx, a)
				}
			}
		}
	}
}

// dockerAppAllowlist returns the package's DockerAppSlugs allowlist as a set, or
// nil when the user has no package or the allowlist is empty (no restriction).
func (r *Reconciler) dockerAppAllowlist(ctx context.Context, userID string) map[string]bool {
	u, err := r.users.FindByID(ctx, userID)
	if err != nil || u == nil || u.PackageID == nil {
		return nil
	}
	pkg, perr := r.packages.FindByID(ctx, *u.PackageID)
	if perr != nil || pkg == nil {
		return nil
	}
	csv := strings.TrimSpace(pkg.DockerAppSlugs)
	if csv == "" {
		return nil
	}
	set := map[string]bool{}
	for _, sl := range strings.Split(csv, ",") {
		if v := strings.TrimSpace(sl); v != "" {
			set[v] = true
		}
	}
	return set
}

func (r *Reconciler) stopDisallowedApp(ctx context.Context, a *models.DockerApp) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := r.agent.Call(callCtx, "docker_app.stop", map[string]any{"slug": a.EffectiveSlug()}); err != nil {
		r.log.Warn("dockerapp: allowlist stop failed", "id", a.ID, "slug", a.Slug, "err", err)
		return
	}
	msg := "stopped: this app is no longer included in the hosting package"
	if err := r.dockerApps.UpdateStatus(ctx, a.ID, models.DockerAppStatusStopped, &msg); err != nil {
		r.log.Warn("dockerapp: allowlist status update failed", "id", a.ID, "err", err)
	}
	r.log.Info("dockerapp: stopped app removed from package allowlist", "id", a.ID, "slug", a.Slug, "user_id", *a.UserID)
	if r.notificationQueue != nil {
		_, _ = r.notificationQueue.Publish(ctx, notifications.Envelope{
			EventKind: "docker_app.removed_from_package",
			Severity:  "warning",
			Title:     fmt.Sprintf("Docker app stopped (not in package): %s", a.Name),
			Body:      fmt.Sprintf("%s was stopped because its app type is no longer included in your hosting package. Contact your administrator.", a.Name),
			Deeplink:  "/jabali-panel/docker-apps",
		})
	}
}

// effectiveDockerLimit is the tenant's allowed running Docker app count. No
// package = 0 (tenant Docker requires a package, Gitea #511); otherwise the
// package's MaxDockerApps.
func (r *Reconciler) effectiveDockerLimit(ctx context.Context, userID string) int {
	u, err := r.users.FindByID(ctx, userID)
	if err != nil || u == nil || u.PackageID == nil {
		return 0
	}
	pkg, perr := r.packages.FindByID(ctx, *u.PackageID)
	if perr != nil || pkg == nil {
		return 0
	}
	return int(pkg.MaxDockerApps)
}

// effectiveDockerDiskQuotaBytes is the tenant's package disk quota in bytes
// (DiskQuotaMB). 0 = unlimited / no package.
func (r *Reconciler) effectiveDockerDiskQuotaBytes(ctx context.Context, userID string) int64 {
	u, err := r.users.FindByID(ctx, userID)
	if err != nil || u == nil || u.PackageID == nil {
		return 0
	}
	pkg, perr := r.packages.FindByID(ctx, *u.PackageID)
	if perr != nil || pkg == nil {
		return 0
	}
	return int64(pkg.DiskQuotaMB) * 1024 * 1024
}

func (r *Reconciler) stopOverDiskQuota(ctx context.Context, a *models.DockerApp) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := r.agent.Call(callCtx, "docker_app.stop", map[string]any{"slug": a.EffectiveSlug()}); err != nil {
		r.log.Warn("dockerapp: disk-quota stop failed", "id", a.ID, "slug", a.Slug, "err", err)
		return
	}
	msg := "stopped: tenant Docker disk usage is over the hosting package quota"
	if err := r.dockerApps.UpdateStatus(ctx, a.ID, models.DockerAppStatusStopped, &msg); err != nil {
		r.log.Warn("dockerapp: disk-quota status update failed", "id", a.ID, "err", err)
	}
	r.log.Info("dockerapp: stopped over-disk-quota app", "id", a.ID, "slug", a.Slug, "user_id", *a.UserID)
	if r.notificationQueue != nil {
		_, _ = r.notificationQueue.Publish(ctx, notifications.Envelope{
			EventKind: "docker_app.disk_quota_stopped",
			Severity:  "warning",
			Title:     fmt.Sprintf("Docker app stopped (disk quota): %s", a.Name),
			Body:      fmt.Sprintf("%s was stopped because your Docker apps exceed your hosting package's disk quota. Delete an app to free space, then start it again.", a.Name),
			Deeplink:  "/jabali-panel/docker-apps",
		})
	}
}

func (r *Reconciler) stopOverEntitlement(ctx context.Context, a *models.DockerApp) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := r.agent.Call(callCtx, "docker_app.stop", map[string]any{"slug": a.EffectiveSlug()}); err != nil {
		r.log.Warn("dockerapp: entitlement stop failed", "id", a.ID, "slug", a.Slug, "err", err)
		return
	}
	msg := "stopped: over the hosting package's Docker app limit"
	if err := r.dockerApps.UpdateStatus(ctx, a.ID, models.DockerAppStatusStopped, &msg); err != nil {
		r.log.Warn("dockerapp: entitlement status update failed", "id", a.ID, "err", err)
	}
	r.log.Info("dockerapp: stopped over-entitlement app", "id", a.ID, "slug", a.Slug, "user_id", *a.UserID)
	if r.notificationQueue != nil {
		_, _ = r.notificationQueue.Publish(ctx, notifications.Envelope{
			EventKind: "docker_app.entitlement_stopped",
			Severity:  "warning",
			Title:     fmt.Sprintf("Docker app stopped: %s", a.Name),
			Body:      fmt.Sprintf("%s was stopped because it exceeds your hosting package's Docker app limit. Ask your administrator to raise the limit to start it again.", a.Name),
			Deeplink:  "/jabali-panel/docker-apps",
		})
	}
}

// dockerAppStuckTimeout is the maximum age of a row in a
// transient state (installing / updating) before the watchdog
// forcibly flips it to `failed`. install path uses a 3-minute
// agent timeout; update uses 6. 15 minutes gives ample headroom
// for slow image pulls without leaving zombie rows forever when
// panel-api crashed or the client disconnected mid-flight.
const dockerAppStuckTimeout = 15 * time.Minute

func (r *Reconciler) reconcileOneDockerApp(ctx context.Context, app *models.DockerApp) {
	// Watchdog: any row stuck in a transient state past the timeout
	// is forcibly failed so the operator can delete/retry from the
	// UI. Without this a panel-api restart or a dropped client
	// connection leaves the row at `installing` forever (see
	// updateImage / install handler -- they swallow status-write
	// errors when the request context was cancelled).
	if (app.Status == models.DockerAppStatusInstalling || app.Status == models.DockerAppStatusUpdating) &&
		time.Since(app.UpdatedAt) > dockerAppStuckTimeout {
		msg := fmt.Sprintf("watchdog: stuck in %s for %s", app.Status, time.Since(app.UpdatedAt).Round(time.Second))
		if err := r.dockerApps.UpdateStatus(ctx, app.ID, models.DockerAppStatusFailed, &msg); err != nil {
			r.log.Warn("dockerapp: watchdog status update failed", "id", app.ID, "err", err)
		} else {
			r.log.Warn("dockerapp: watchdog flipped stuck row", "id", app.ID, "slug", app.Slug, "from", app.Status, "age", time.Since(app.UpdatedAt))
		}
		return
	}

	switch app.Status {
	case models.DockerAppStatusPending:
		// Install dispatch is owned by the REST handler when it
		// creates the row (so the operator sees an immediate response).
		// The reconciler only retries when an install crash-loop left
		// the row stuck in pending. Dispatch once per tick at most.
		r.dispatchInstall(ctx, app)
	case models.DockerAppStatusInstalling, models.DockerAppStatusRunning, models.DockerAppStatusStopped:
		// Status-poll; the agent verb is cheap (one docker compose ps).
		// Stopped is included so the row catches up when the operator
		// (or a host-reboot autostart) brought the container back up
		// outside the panel -- otherwise the UI shows stale "stopped"
		// indefinitely.
		r.statusPoll(ctx, app)
		// JAB-121 phase 2: (re)assert this app's host logrotate snippet once per
		// panel-api lifetime, so apps installed before the retention feature
		// converge to bounded logs without a reinstall.
		r.ensureLogrotate(ctx, app)
		// M48 Phase 7 follow-up: image-digest poll. Cheap (~1 HTTP
		// HEAD via `docker manifest inspect`) but gated by
		// last_check_at so each app gets checked at most once per
		// dockerAppCheckInterval. When the upstream digest moves
		// AND update_mode='auto', dispatch the update flow.
		r.pollImageUpdate(ctx, app)
	default:
		// stopped / failed / deleted / updating / rolling_back are
		// terminal-or-in-flight from this tick's perspective.
	}
}

// dispatchInstall calls docker_app.install with the rendered compose
// the REST handler stashed under the app row. The reconciler is
// idempotent: if the agent already wrote compose.yml, install just
// runs `docker compose up -d` again, which is a no-op when the
// container is already running.
//
// NOTE: in Phase 3 we use a minimal install payload that the agent
// can fulfil from on-disk state alone. The REST handler is the
// primary install driver; this is the recovery path.
func (r *Reconciler) dispatchInstall(ctx context.Context, app *models.DockerApp) {
	// 3-minute deadline for the whole call. docker pull on a fresh
	// host can take a while; the agent has its own internal timeout
	// for the healthcheck wait.
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	// Mark in-flight so a parallel tick doesn't double-dispatch.
	if err := r.dockerApps.UpdateStatus(callCtx, app.ID, models.DockerAppStatusInstalling, nil); err != nil {
		r.log.Warn("dockerapp: status->installing failed", "id", app.ID, "err", err)
		return
	}

	// Bare-minimum recovery payload — slug only. The agent reads
	// compose.yml from disk and brings it up. The full install
	// payload is set by the REST handler on first dispatch.
	raw, err := r.agent.Call(callCtx, "docker_app.install", map[string]any{
		"slug":                        app.EffectiveSlug(),
		"compose_yml":                 "RECOVERY", // sentinel: agent recovery path reads compose.yml from disk
		"volumes":                     []string{},
		"wait_healthy":                false,
		"healthcheck_timeout_seconds": 0,
	})
	if err != nil {
		errMsg := firstLineString(err.Error())
		_ = r.dockerApps.UpdateStatus(ctx, app.ID, models.DockerAppStatusFailed, &errMsg)
		r.log.Warn("dockerapp: install dispatch failed", "id", app.ID, "slug", app.Slug, "err", err)
		return
	}
	r.log.Info("dockerapp: install dispatched", "id", app.ID, "slug", app.Slug, "raw_len", len(raw))
	// Status converges via the next tick's statusPoll.
}

// statusPoll asks the agent for current docker state and writes
// whatever it learns back to the row. Cheap; safe to run on every
// tick.
func (r *Reconciler) statusPoll(ctx context.Context, app *models.DockerApp) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Refresh the on-disk data size at most every dockerAppSizeInterval —
	// du(1) walks the tree, so it must not run on every cheap status tick.
	wantSize := app.SizeCheckedAt == nil || time.Now().Sub(*app.SizeCheckedAt) >= dockerAppSizeInterval
	raw, err := r.agent.Call(callCtx, "docker_app.status", map[string]any{
		"slug":      app.EffectiveSlug(),
		"with_size": wantSize,
	})
	if err != nil {
		// Don't mark failed — could be a transient agent socket
		// blip. Log + leave the row alone.
		r.log.Debug("dockerapp: status call failed", "id", app.ID, "err", err)
		return
	}
	var resp struct {
		Slug      string `json:"slug"`
		Present   bool   `json:"present"`
		Running   bool   `json:"running"`
		Health    string `json:"health"`
		DataBytes int64  `json:"data_bytes"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		r.log.Warn("dockerapp: parse status failed", "id", app.ID, "err", err)
		return
	}

	// Persist the size when we asked for it and got a real value. >0 guards
	// against a du failure (agent returns 0 best-effort) clobbering a prior
	// reading; a real install dir is always >0 (it holds compose.yml + .env).
	if wantSize && resp.Present && resp.DataBytes > 0 {
		if err := r.dockerApps.UpdateDataSize(ctx, app.ID, resp.DataBytes); err != nil {
			r.log.Warn("dockerapp: data-size update failed", "id", app.ID, "err", err)
		}
	}

	target := app.Status
	switch {
	case !resp.Present:
		// Compose dir is gone — operator probably purged it from
		// shell. We treat as `failed` so the UI doesn't claim it's
		// running.
		target = models.DockerAppStatusFailed
	case resp.Running && (resp.Health == "healthy" || resp.Health == "none"):
		target = models.DockerAppStatusRunning
	case resp.Running:
		// Started but not healthy yet; keep `installing` so the UI
		// shows progress.
		target = models.DockerAppStatusInstalling
	default:
		target = models.DockerAppStatusStopped
	}
	if target == app.Status {
		return
	}
	// Clear last_error when transitioning to a healthy terminal state.
	// Without this, a transient first-install healthcheck timeout leaves
	// the err chip lit forever even after the container becomes ready.
	var errArg *string
	if target == models.DockerAppStatusRunning && app.LastError != nil && *app.LastError != "" {
		empty := ""
		errArg = &empty
	}
	if err := r.dockerApps.UpdateStatus(ctx, app.ID, target, errArg); err != nil {
		r.log.Warn("dockerapp: status update failed", "id", app.ID, "from", app.Status, "to", target, "err", err)
	}
}

// firstLineString is a copy of firstLine but typed for string input,
// avoiding the dependency on reconciler.go's parameter shape.
func firstLineString(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// WithDockerApps wires the docker-app repository into the reconciler.
// Phase 1 ships the repo + tables; this hook lights up the tick.
func (r *Reconciler) WithDockerApps(repo repository.DockerAppRepository) *Reconciler {
	r.dockerApps = repo
	return r
}

// WithDockerCatalog wires the loaded M48 catalog so the
// image-update poller can resolve docker_apps.slug to the catalog's
// image_channel. Nil-safe; without it the poller no-ops.
func (r *Reconciler) WithDockerCatalog(cat *dockerapp.Catalog) *Reconciler {
	r.dockerCatalog = cat
	return r
}

// compile-time guard: keep fmt imported even when only used for
// stringification in future Phase 7 work.
var _ = fmt.Sprintf

// ensureLogrotate (JAB-121 phase 2) asks the agent to (re)write this app's
// host logrotate snippet from its catalog rotate policy. Runs at most once per
// app per panel-api lifetime (the in-memory set clears on restart, so a jabali
// update re-converges every installed app). Apps with no log volumes are
// marked done without an agent round-trip.
func (r *Reconciler) ensureLogrotate(ctx context.Context, app *models.DockerApp) {
	if app == nil || r.dockerCatalog == nil || r.agent == nil {
		return
	}
	if _, done := r.logrotateEnsured.Load(app.ID); done {
		return
	}
	entry, ok := r.dockerCatalog.Get(app.Slug)
	if !ok {
		return
	}
	specs := make([]map[string]any, 0)
	for _, v := range entry.Volumes {
		if v.Rotate != nil {
			specs = append(specs, map[string]any{
				"volume":         v.Name,
				"retention_days": v.Rotate.RetentionDays,
				"max_size":       v.Rotate.MaxSize,
				"mode":           v.Rotate.Mode,
			})
		}
	}
	// Nothing to rotate → mark done (no per-tick re-check) and skip the agent.
	if len(specs) == 0 {
		r.logrotateEnsured.Store(app.ID, true)
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := r.agent.Call(callCtx, "docker_app.logrotate_ensure", map[string]any{
		"slug":       app.EffectiveSlug(),
		"log_rotate": specs,
	}); err != nil {
		r.log.Warn("dockerapp: logrotate ensure failed", "id", app.ID, "slug", app.Slug, "err", err)
		return // leave unmarked so the next tick retries
	}
	r.logrotateEnsured.Store(app.ID, true)
}

// dockerAppCheckInterval is the per-app poll cadence for upstream
// image digests. 6h is conservative; busy registries (docker hub
// rate limits) appreciate the lower load and operators don't
// usually need sub-day update awareness.
const dockerAppCheckInterval = 6 * time.Hour

// pollImageUpdate is the per-tick check-for-updates pass. Gated by
// last_check_at so the reconciler can't pound the registry on every
// tick. Sets available_digest when the upstream has moved; when
// update_mode='auto' AND status='running' AND last_error IS NULL,
// dispatches docker_app.update (which the agent runs with snapshot
// + healthcheck + rollback).
func (r *Reconciler) pollImageUpdate(ctx context.Context, app *models.DockerApp) {
	if app == nil || r.dockerCatalog == nil || r.dockerApps == nil {
		return
	}
	if app.LastCheckAt != nil && time.Since(*app.LastCheckAt) < dockerAppCheckInterval {
		return
	}
	entry, ok := r.dockerCatalog.Get(app.Slug)
	if !ok {
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := r.agent.Call(callCtx, "docker_app.check_update", map[string]any{
		"slug":          app.EffectiveSlug(),
		"image_channel": entry.ImageChannel,
	})
	// Always mark checked even on failure -- we don\'t want a
	// permanently-failing registry to spin the poller hot.
	defer func() {
		_ = r.dockerApps.MarkChecked(context.Background(), app.ID)
	}()
	if err != nil {
		r.log.Debug("dockerapp: check_update failed", "id", app.ID, "err", err)
		return
	}
	var resp struct {
		AvailableDigest string `json:"available_digest"`
		UpdateAvailable bool   `json:"update_available"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		r.log.Warn("dockerapp: parse check_update failed", "id", app.ID, "err", err)
		return
	}
	if !resp.UpdateAvailable {
		// Converge stale state: clear available_digest if it was
		// previously set, so the UI chip drops once upstream caught
		// up (or once an update fully completed and upstream now
		// matches local).
		if app.AvailableDigest != nil && *app.AvailableDigest != "" {
			_ = r.dockerApps.UpdateAvailableDigest(ctx, app.ID, "")
		}
		return
	}
	_ = r.dockerApps.UpdateAvailableDigest(ctx, app.ID, resp.AvailableDigest)
	r.log.Info("dockerapp: update available", "id", app.ID, "slug", app.Slug, "digest", resp.AvailableDigest)

	// Fire docker_app.update_available so the operator's configured
	// channels (email / Slack / ntfy) light up. Title carries the
	// app's display name; body has the digest prefix.
	if r.notificationQueue != nil {
		body := fmt.Sprintf("A new image is available for %s (%s). New digest: %s. Update mode: %s.",
			app.Name, app.Slug, shortDigest(resp.AvailableDigest), app.UpdateMode)
		_, perr := r.notificationQueue.Publish(ctx, notifications.Envelope{
			EventKind: "docker_app.update_available",
			Severity:  "info",
			Title:     fmt.Sprintf("Docker app update available: %s", app.Name),
			Body:      body,
			Deeplink:  "/jabali-admin/docker-apps",
		})
		if perr != nil {
			r.log.Warn("dockerapp: publish update_available failed", "id", app.ID, "err", perr)
		}
	}

	// Auto-update gate. Manual mode just leaves the digest on the
	// row; the UI surfaces it as "update available". Auto dispatches
	// the same agent verb the operator would click.
	if app.UpdateMode == models.DockerAppUpdateModeAuto && app.LastError == nil && app.Status == models.DockerAppStatusRunning {
		r.log.Info("dockerapp: auto-update dispatching", "id", app.ID, "slug", app.Slug)
		updateCtx, ucancel := context.WithTimeout(ctx, 6*time.Minute)
		defer ucancel()
		updateParams := map[string]any{
			"slug":                        app.EffectiveSlug(),
			"healthcheck_timeout_seconds": 300,
		}
		// Tenant-owned app (Gitea #510): make the agent re-run the tenant
		// compose safety gate on the on-disk compose before `up`, so an
		// auto-update can never bring up an unhardened/unsafe compose — it
		// fails closed instead. Mirrors the manual updateImage path.
		if app.UserID != nil {
			updateParams["tenant_validate"] = true
			updateParams["tenant_caps"] = dockerapp.TenantCapAllowlist(entry.TenantCaps)
		}
		_, err := r.agent.Call(updateCtx, "docker_app.update", updateParams)
		if err != nil {
			msg := firstLineString(err.Error())
			_ = r.dockerApps.UpdateStatus(ctx, app.ID, models.DockerAppStatusFailed, &msg)
		}
	}
}

// shortDigest trims a `sha256:abcdef...` to a 12-char view for
// notifications and log lines.
func shortDigest(d string) string {
	const prefix = "sha256:"
	if len(d) > len(prefix) {
		d = d[len(prefix):]
	}
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
