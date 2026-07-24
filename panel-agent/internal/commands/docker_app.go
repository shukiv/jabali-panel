// docker_app.* agent verbs — orchestrate Docker for the M48 App
// Marketplace. The agent NEVER reads the catalog itself; it receives
// the rendered compose YAML + the runtime params it needs from the
// panel-api side. That keeps the catalog as a single source of
// truth in panel-api/internal/dockerapp.
//
// Security boundary: this is the ONLY file (with its sibling
// docker_app_lifecycle.go) that issues docker commands. The agent
// runs as root and has access to /var/run/docker.sock; the panel-api
// process does not. Verbs that take a slug always validate it
// against the allowed-charset regex below before any file or shell
// operation.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dockerAppDataRoot is the parent of every per-app data directory.
// install.sh creates this with mode 0750, owner root:jabali.
const dockerAppDataRoot = "/var/lib/jabali/docker-apps"

// dockerAppSlugRE matches ADR-0116 Decision 5's slug rule. The regex
// is anchored AND charset-restricted so a malicious slug cannot
// escape filesystem ops (no ../, no quoting tricks).
//
// Length cap of 52 chars (1 leading letter + up to 51 trailing) keeps
// the resulting docker container hostname (jabali-app-<slug>) under
// the RFC 1123 DNS label limit of 63 chars -- the implicit container
// hostname must be a valid DNS label or compose refuses to start.
// Prior cap of 32 chars dated from the single-install era; M48
// multi-install derives slugs as "<catalog>-<install_name>" which
// can run longer.
var dockerAppSlugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,51}$`)

// validateSlug returns an InvalidArgument error if the slug fails
// the catalog-charset constraint.
func validateSlug(slug string) error {
	if !dockerAppSlugRE.MatchString(slug) {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid slug %q: must match ^[a-z][a-z0-9-]{1,31}$", slug),
		}
	}
	return nil
}

// dockerAppInstallParams is the input shape for docker_app.install.
type dockerAppInstallParams struct {
	Slug        string            `json:"slug"`
	ComposeYML  string            `json:"compose_yml"`            // rendered template
	EnvFile     string            `json:"env_file,omitempty"`     // .env contents (secrets); empty = no .env
	Volumes     []string          `json:"volumes"`                // subdirs to create under DataRoot
	VolumeOwner string            `json:"volume_owner,omitempty"` // optional "uid:gid" applied to the data root + each volume subdir before compose-up
	WaitHealth  bool              `json:"wait_healthy,omitempty"`
	HealthTO    int               `json:"healthcheck_timeout_seconds,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"` // free-form tags; written into a sidecar JSON for ops
	// M49 (GH #170): when TenantValidate is true the agent runs the tenant
	// safety gate (docker compose config -> validateTenantCompose) before
	// `up`, rejecting privileged / out-of-allowlist caps / host bind-mounts.
	// TenantCaps is the catalog-verified capability allowlist for this app.
	TenantValidate bool     `json:"tenant_validate,omitempty"`
	TenantCaps     []string `json:"tenant_caps,omitempty"`
	// TenantCgroup is the exact owner slice the compose must declare (Gitea #525).
	TenantCgroup string `json:"tenant_cgroup,omitempty"`
	// LogRotate (JAB-121) lists persistent log volumes to cover with a host
	// logrotate snippet at install so their file logs — which bypass Docker's
	// journald driver — stay bounded on tenant disk. Empty = no snippet.
	LogRotate []dockerAppLogRotate `json:"log_rotate,omitempty"`
}

// dockerAppLogRotate is one persistent log volume's retention policy (JAB-121).
type dockerAppLogRotate struct {
	Volume        string `json:"volume"`
	RetentionDays int    `json:"retention_days,omitempty"`
	MaxSize       string `json:"max_size,omitempty"`
	Mode          string `json:"mode,omitempty"`
}

type dockerAppInstallResponse struct {
	Slug        string `json:"slug"`
	ContainerID string `json:"container_id,omitempty"`
	Status      string `json:"status"` // "running" | "starting" | "unhealthy"
}

// dockerAppInstallHandler writes the rendered compose + env file to
// /var/lib/jabali/docker-apps/<slug>/, creates the requested volume
// subdirectories, and runs `docker compose up -d`. When
// wait_healthy=true, blocks until the container reports healthy or
// the healthcheck_timeout_seconds budget runs out.
func dockerAppInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerAppInstallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("parse: %v", err),
		}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	if p.ComposeYML == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "compose_yml is required",
		}
	}

	dir := filepath.Join(dockerAppDataRoot, p.Slug)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v", dir, err)}
	}

	// Volume subdirectories (catalog-declared list).
	for _, v := range p.Volumes {
		if !dockerAppSlugRE.MatchString(v) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("invalid volume name %q", v),
			}
		}
		vd := filepath.Join(dir, v)
		if err := os.MkdirAll(vd, 0o750); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v", vd, err)}
		}
	}

	// JAB-121: install a host logrotate snippet for declared persistent log
	// volumes so their file logs (which bypass Docker's journald driver) stay
	// bounded on tenant disk. Best-effort: a logrotate write failure must NOT
	// fail the install — the app still works, the logs just are not rotated yet
	// (a re-install/update re-asserts the snippet).
	if err := writeDockerAppLogrotate(p.Slug, dir, p.LogRotate); err != nil {
		fmt.Fprintf(os.Stderr, "docker_app.install: logrotate snippet for %s: %v\n", p.Slug, err)
	}

	// Chown the data root + each volume subdir so the container can write its
	// own bind-mounted state. Two reasons:
	//   - images whose entrypoint runs as a non-root UID EACCES on their own
	//     config otherwise (Gitea's /data/gitea/conf/app.ini is the scar);
	//   - under docker userns-remap the container's UIDs are shifted into the
	//     dockremap subuid range. Docker fixes named-volume ownership for that
	//     automatically but NOT bind mounts, and every jabali app uses bind
	//     mounts under dockerAppDataRoot -- so without the offset the container's
	//     (remapped) root EACCES on its data dir and init dies with
	//     "chdir to cwd ... permission denied" (GH #284).
	// base==0 when remap is off: the resolved owner is then the legacy behaviour
	// exactly (chown only when the catalog declares volume_owner, no offset).
	uid, gid, applyOwner, perr := resolveInstallOwner(p.VolumeOwner, dockremapRemapBase())
	if perr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid volume_owner %q: %v", p.VolumeOwner, perr),
		}
	}
	if applyOwner {
		if err := os.Chown(dir, uid, gid); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown %s: %v", dir, err)}
		}
		for _, v := range p.Volumes {
			vd := filepath.Join(dir, v)
			// Walk the subtree -- nested state under a compose bind
			// mount (gitea data/git, nextcloud var/www/html/data, ...)
			// needs the same ownership. The volume tree is writable by the
			// app container, so a container can plant a symlink to a host
			// path (e.g. /etc/shadow); use lchown so we change the link
			// itself, never the symlink's target (Gitea #479).
			if werr := lchownTree(vd, uid, gid); werr != nil {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown %s: %v", vd, werr)}
			}
		}
	}

	// Atomic write: tmp + rename so a crashed install never leaves a
	// partial compose.yml that docker compose would try to parse.
	if err := writeAtomicDockerApp(filepath.Join(dir, "compose.yml"), []byte(p.ComposeYML), 0o640); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if p.EnvFile != "" {
		if err := writeAtomicDockerApp(filepath.Join(dir, ".env"), []byte(p.EnvFile), 0o600); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
		}
	}
	if len(p.Metadata) > 0 {
		metaBytes, _ := json.MarshalIndent(p.Metadata, "", "  ")
		_ = writeAtomicDockerApp(filepath.Join(dir, ".jabali-meta.json"), metaBytes, 0o644)
	}

	// M49 tenant safety gate: validate the RESOLVED compose before up, so a
	// privileged container / foreign capability / host bind-mount can never be
	// brought up for a tenant even if the rendered compose was wrong.
	if p.TenantValidate {
		if err := runTenantComposeValidation(ctx, dir, p.TenantCaps, p.TenantCgroup); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "tenant compose rejected: " + err.Error()}
		}
	}

	// docker compose up -d. ctx covers the whole operation incl.
	// the optional health-wait below; the caller is responsible for
	// passing a context with a sensible deadline.
	//
	// GH #170 #2 / GH #284: a TENANT install is gated to CATALOG apps only
	// (the slug allowlist + tenant_installable filter), so its image is always
	// a curated, catalog-pinned reference — never arbitrary tenant input. Use
	// `--pull missing` so the install pulls that one image if the admin hasn't
	// pre-provisioned it, instead of failing with a confusing "never started"
	// (was `--pull never`, which stranded every un-provisioned tenant install).
	upArgs := []string{"up", "-d"}
	if p.TenantValidate {
		upArgs = append(upArgs, "--pull", "missing")
	}
	if out, err := runDockerCompose(ctx, dir, upArgs...); err != nil {
		msg := composeFailMessage("up -d", out, err)
		if p.TenantValidate && (strings.Contains(out, "pull") || strings.Contains(out, "manifest unknown") || strings.Contains(out, "not found")) {
			msg = "could not fetch this app's image — check the host has registry access, then retry: " + msg
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: msg,
			Details: json.RawMessage(fmt.Sprintf(`{"stderr": %q}`, redactComposeOutput(out))),
		}
	}

	status := "starting"
	containerID := ""
	if p.WaitHealth {
		timeout := time.Duration(p.HealthTO) * time.Second
		if timeout <= 0 {
			timeout = 90 * time.Second
		}
		s, id := waitHealthy(ctx, dir, p.Slug, timeout)
		status = s
		containerID = id
	}

	return dockerAppInstallResponse{Slug: p.Slug, ContainerID: containerID, Status: status}, nil
}

// dockerAppLifecycleParams covers start/stop/restart/rebuild/delete.
type dockerAppLifecycleParams struct {
	Slug         string `json:"slug"`
	PurgeVolumes bool   `json:"purge_volumes,omitempty"` // delete only
	// TenantValidate / TenantCaps (Gitea #513): when a tenant-owned app is
	// brought back up (start/restart/rebuild), re-run the tenant compose safety
	// gate on the on-disk compose before `up`.
	TenantValidate bool     `json:"tenant_validate,omitempty"`
	TenantCaps     []string `json:"tenant_caps,omitempty"`
	// TenantCgroup is the exact owner slice the compose must declare (Gitea #525).
	TenantCgroup string `json:"tenant_cgroup,omitempty"`
}

type dockerAppLifecycleResponse struct {
	Slug   string `json:"slug"`
	Status string `json:"status"` // "started" | "stopped" | "restarted" | "rebuilt" | "deleted"
}

func dockerAppStartHandler(ctx context.Context, params json.RawMessage) (any, error) {
	// `up -d` instead of `start`: idempotent over the (re)create
	// step too, so it survives the scar where a prior failed install
	// (or a host reboot orphan) left containers with our slug names
	// behind. `compose start` only re-runs an exact match by ID and
	// 409s with "container name in use" on any mismatch.
	return runLifecycle(ctx, params, "started", "up", "-d")
}
func dockerAppStopHandler(ctx context.Context, params json.RawMessage) (any, error) {
	return runLifecycle(ctx, params, "stopped", "stop")
}
func dockerAppRestartHandler(ctx context.Context, params json.RawMessage) (any, error) {
	// `up -d` so a restart after a stale-container scar still wins.
	// Stopping the container directly via `restart` doesn't help if
	// the container was created with a different config hash.
	return runLifecycle(ctx, params, "restarted", "up", "-d")
}
func dockerAppRebuildHandler(ctx context.Context, params json.RawMessage) (any, error) {
	return runLifecycle(ctx, params, "rebuilt", "up", "-d", "--force-recreate")
}

func runLifecycle(ctx context.Context, params json.RawMessage, statusOnSuccess string, composeArgs ...string) (any, error) {
	var p dockerAppLifecycleParams
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
	// Re-validate the tenant gate before any bring-up (Gitea #513): start /
	// restart / rebuild all `compose up` from the on-disk compose, which could
	// be stale or unhardened. Fail-safe (leave it down) rather than start it.
	if p.TenantValidate && len(composeArgs) > 0 && composeArgs[0] == "up" {
		if err := runTenantComposeValidation(ctx, dir, p.TenantCaps, p.TenantCgroup); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: fmt.Sprintf("tenant compose validation failed: %v", err)}
		}
	}
	if out, err := runDockerCompose(ctx, dir, composeArgs...); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("docker compose %v failed: %v", composeArgs, err),
			Details: json.RawMessage(fmt.Sprintf(`{"stderr": %q}`, redactComposeOutput(out))),
		}
	}
	return dockerAppLifecycleResponse{Slug: p.Slug, Status: statusOnSuccess}, nil
}

func dockerAppDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerAppLifecycleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	dir := filepath.Join(dockerAppDataRoot, p.Slug)
	// JAB-121: drop this app's host logrotate snippet (best-effort).
	removeDockerAppLogrotate(p.Slug)
	// docker compose down. -v removes named volumes too; we use bind
	// mounts under DataRoot so this is mostly defensive. A `down` failure
	// is remembered, NOT returned early: a failed/half-broken install still
	// leaves a data dir we must reclaim when a purge is requested, so the
	// purge below has to run first. The error is surfaced afterwards.
	var downErr *agentwire.AgentError
	if _, err := os.Stat(filepath.Join(dir, "compose.yml")); err == nil {
		if out, err := runDockerCompose(ctx, dir, "down", "-v"); err != nil {
			downErr = &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("docker compose down failed: %v", err),
				Details: json.RawMessage(fmt.Sprintf(`{"stderr": %q}`, redactComposeOutput(out))),
			}
		}
	}
	if p.PurgeVolumes {
		// Defensive: only ever remove paths under dockerAppDataRoot.
		// validateSlug above bounds the slug to the allowed charset.
		if err := os.RemoveAll(dir); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("rm -rf %s: %v", dir, err)}
		}
	}
	if downErr != nil {
		return nil, downErr
	}
	return dockerAppLifecycleResponse{Slug: p.Slug, Status: "deleted"}, nil
}

// dockerAppStatusParams is the input for the lightweight status verb
// used by the reconciler to converge state.
type dockerAppStatusParams struct {
	Slug string `json:"slug"`
	// WithSize triggers a du(1) of the install data dir. Gated by the
	// caller because it walks the tree — the reconciler only sets it on a
	// slow cadence, never on every status tick.
	WithSize bool `json:"with_size,omitempty"`
}

type dockerAppStatusResponse struct {
	Slug      string            `json:"slug"`
	Present   bool              `json:"present"`              // compose.yml exists on disk
	Running   bool              `json:"running"`              // at least one service is in 'running' state
	Health    string            `json:"health"`               // "healthy" | "unhealthy" | "starting" | "none"
	DataBytes int64             `json:"data_bytes,omitempty"` // du of the install dir; only when with_size
	Lines     []dockerAppPsLine `json:"lines,omitempty"`
}

type dockerAppPsLine struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Status string `json:"status"`
	Health string `json:"health,omitempty"`
}

func dockerAppStatusHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerAppStatusParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	dir := filepath.Join(dockerAppDataRoot, p.Slug)
	resp := dockerAppStatusResponse{Slug: p.Slug, Health: "none"}
	if _, err := os.Stat(filepath.Join(dir, "compose.yml")); err != nil {
		// Not installed; respond truthfully.
		return resp, nil
	}
	resp.Present = true
	// docker compose ps --format json
	out, err := runDockerCompose(ctx, dir, "ps", "--format", "json")
	if err != nil {
		// Treat as "compose dir exists but docker can't read state".
		// The reconciler will retry; surface the stderr in last_error.
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("docker compose ps failed: %v", err),
			Details: json.RawMessage(fmt.Sprintf(`{"stderr": %q}`, redactComposeOutput(out))),
		}
	}
	lines := parseComposePSJSON(out)
	resp.Lines = lines
	for _, l := range lines {
		if l.State == "running" {
			resp.Running = true
		}
		if l.Health == "healthy" {
			resp.Health = "healthy"
		} else if resp.Health == "none" && l.Health != "" {
			resp.Health = l.Health
		}
	}
	// The in-container docker healthcheck is unreliable across the catalog
	// (missing tool / wrong port), so when the app publishes a loopback port,
	// the host-side HTTP probe is the source of truth for health — a serving
	// app reads "healthy" even if its docker healthcheck is red.
	if ports := probeLoopbackPorts(dir); len(ports) > 0 && resp.Running {
		if httpServing(ctx, ports) {
			resp.Health = "healthy"
		} else if resp.Health == "none" || resp.Health == "" {
			resp.Health = "unhealthy"
		}
	}
	if p.WithSize {
		// Best-effort: a du failure must not fail the status call.
		if n, derr := dirSizeBytes(ctx, dir); derr == nil {
			resp.DataBytes = n
		}
	}
	return resp, nil
}

// dirSizeBytes returns the apparent on-disk size of dir in bytes via
// du(1). One exec, no per-file fork; bounded by the tree walk. Returns
// an error the caller can ignore (size is advisory, never load-bearing).
func dirSizeBytes(ctx context.Context, dir string) (int64, error) {
	out, err := exec.CommandContext(ctx, "du", "-sb", dir).Output()
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, fmt.Errorf("du: empty output")
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("du: parse %q: %w", fields[0], err)
	}
	return n, nil
}

// --- helpers -----------------------------------------------------------------

// runDockerCompose executes `docker compose <args...>` in dir. Returns
// the combined stdout+stderr, plus any exec error.
func runDockerCompose(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"compose"}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// composeFailMessage builds a user-facing error that includes the TAIL of the
// docker compose output, so the panel surfaces the real reason (which container
// failed, an image-pull error, a MariaDB native-AIO crash inside LXC, ...)
// instead of a bare "exit status 1". The full output still rides along in the
// AgentError Details. GH#178.
// redactComposeOutput (GH #700) strips jabali-internal + system-secret absolute
// paths from surfaced subprocess output so a compose failure shown to the caller
// doesn't leak host layout. General paths are kept for diagnosability.
var composeSecretPathRe = regexp.MustCompile(`/(?:root|etc/jabali[A-Za-z0-9_./-]*|run/jabali[A-Za-z0-9_./-]*|var/lib/jabali[A-Za-z0-9_./-]*|etc/(?:shadow|gshadow|kratos)[A-Za-z0-9_./-]*)`)

func redactComposeOutput(s string) string {
	return composeSecretPathRe.ReplaceAllString(s, "[redacted]")
}

func composeFailMessage(action, out string, err error) string {
	out = redactComposeOutput(out)
	tail := lastNonEmptyLines(out, 8)
	if tail == "" {
		return fmt.Sprintf("docker compose %s failed: %v", action, err)
	}
	return fmt.Sprintf("docker compose %s failed: %v\n%s", action, err, tail)
}

// lastNonEmptyLines returns the last up-to-n non-blank lines of s, joined with
// newlines — enough signal to diagnose a compose failure without dumping the
// whole (often noisy) output into the error message.
func lastNonEmptyLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		if t := strings.TrimSpace(ln); t != "" {
			kept = append(kept, t)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, "\n")
}

// writeAtomic writes data to a temp file in the same dir, fsyncs,
// and renames into place. Crash-safe.
func writeAtomicDockerApp(dst string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".jabali-app-*.tmp")
	if err != nil {
		return fmt.Errorf("CreateTemp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("rename %s: %w", dst, err)
	}
	return nil
}

// waitHealthy polls `docker compose ps --format json` until the first
// service reports `healthy`, or until the timeout elapses. Returns
// the resolved status string and the container ID (if known).
func waitHealthy(ctx context.Context, dir, slug string, timeout time.Duration) (string, string) {
	// Prefer a host-side HTTP probe of the app's published port over the
	// in-container docker healthcheck: the latter depends on a tool the image
	// may not ship (wget) and on a port that may be mis-set in the catalog,
	// which silently wedged installs at "installing". The HTTP probe asks the
	// app directly. Docker-health is the fallback for installs that publish no
	// loopback HTTP port.
	ports := probeLoopbackPorts(dir)
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return "starting", ""
		default:
		}
		if len(ports) > 0 {
			if httpServing(ctx, ports) {
				return "running", ""
			}
		} else {
			out, err := runDockerCompose(ctx, dir, "ps", "--format", "json")
			if err == nil {
				for _, l := range parseComposePSJSON(out) {
					if l.Health == "healthy" {
						return "running", l.Name
					}
					if l.Health == "unhealthy" {
						return "unhealthy", l.Name
					}
				}
			}
		}
		if time.Now().After(deadline) {
			if len(ports) > 0 {
				// App never answered an HTTP request on its published port —
				// not serving (image broken, or the catalog container_port is
				// wrong). Fail fast with a clear state instead of hanging.
				return "unhealthy", ""
			}
			return "starting", ""
		}
		time.Sleep(2 * time.Second)
	}
}

// parseComposePSJSON tolerates BOTH the legacy stream-of-objects
// format (one JSON per line) and the array format. `docker compose
// ps --format json` shipped both shapes across versions.
func parseComposePSJSON(s string) []dockerAppPsLine {
	if s == "" {
		return nil
	}
	out := make([]dockerAppPsLine, 0)
	// Try array first (newer compose).
	var arr []map[string]any
	if err := json.Unmarshal([]byte(s), &arr); err == nil {
		for _, m := range arr {
			out = append(out, lineFromMap(m))
		}
		return out
	}
	// Fall back to NDJSON.
	for _, line := range splitLines(s) {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err == nil {
			out = append(out, lineFromMap(m))
		}
	}
	return out
}

func lineFromMap(m map[string]any) dockerAppPsLine {
	get := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	return dockerAppPsLine{
		Name:   get("Name"),
		State:  get("State"),
		Status: get("Status"),
		Health: get("Health"),
	}
}

func splitLines(s string) []string {
	out := make([]string, 0)
	cur := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[cur:i])
			cur = i + 1
		}
	}
	if cur < len(s) {
		out = append(out, s[cur:])
	}
	return out
}

// Register all docker_app verbs at init time.
func init() {
	Default.Register("docker_app.install", dockerAppInstallHandler)
	Default.Register("docker_app.read_env", dockerAppReadEnvHandler)
	Default.Register("docker_app.start", dockerAppStartHandler)
	Default.Register("docker_app.stop", dockerAppStopHandler)
	Default.Register("docker_app.restart", dockerAppRestartHandler)
	Default.Register("docker_app.rebuild", dockerAppRebuildHandler)
	Default.Register("docker_app.delete", dockerAppDeleteHandler)
	Default.Register("docker_app.status", dockerAppStatusHandler)
}

// Compile-time: silence "imported and not used" if strconv ever gets
// removed by a refactor. Keep the import so future expansion (timeouts,
// percentile parsing) doesn't trip over a missing dependency.
var _ = strconv.Itoa

// parseUIDGID splits "uid:gid" into ints. Both halves must be
// numeric; we do NOT resolve usernames to uids because the agent
// would have to honour either the host's /etc/passwd or the image's
// own UID convention -- ambiguous, brittle, and the catalog already
// hardcodes the image's expected UID anyway.
// dockremapRemapBase returns the dockremap subuid base when docker userns-remap
// is active on this host, or 0 when it is off (or undeterminable). Active iff
// /etc/docker/daemon.json sets a non-empty "userns-remap"; the base is then the
// dockremap entry in /etc/subuid. Any read/parse miss returns 0 -- ownership is
// never offset on uncertainty (that would break a non-remap host).
func dockremapRemapBase() int {
	daemon, err := os.ReadFile("/etc/docker/daemon.json")
	if err != nil {
		return 0
	}
	var cfg struct {
		UsernsRemap string `json:"userns-remap"`
	}
	if err := json.Unmarshal(daemon, &cfg); err != nil || strings.TrimSpace(cfg.UsernsRemap) == "" {
		return 0
	}
	subuid, err := os.ReadFile("/etc/subuid")
	if err != nil {
		return 0
	}
	return parseDockremapSubuidBase(string(subuid))
}

// parseDockremapSubuidBase pulls the base subuid for the dockremap user out of
// an /etc/subuid body (lines "name:base:count"); 0 if absent/unparseable.
// Mirrors the enable-tenant retrofit resolver. Pure for testing.
func parseDockremapSubuidBase(subuidBody string) int {
	for _, line := range strings.Split(subuidBody, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) >= 2 && parts[0] == "dockremap" {
			if base, err := strconv.Atoi(parts[1]); err == nil {
				return base
			}
		}
	}
	return 0
}

// resolveInstallOwner computes the on-disk owner for an app's data tree from the
// catalog volume_owner ("" or "uid:gid") and the userns-remap base (0 = remap
// off). base==0 reproduces the legacy behaviour exactly: chown only when a
// volume_owner is declared, no offset. base>0 shifts the (container-internal)
// owner into the dockremap range -- defaulting to root 0:0 -> base:base when no
// volume_owner is declared (the common case, e.g. memos), which is what the
// container's remapped root needs to reach its bind mount.
func resolveInstallOwner(volumeOwner string, base int) (uid, gid int, apply bool, err error) {
	if volumeOwner != "" {
		u, g, perr := parseUIDGID(volumeOwner)
		if perr != nil {
			return 0, 0, false, perr
		}
		uid, gid, apply = u, g, true
	}
	if base > 0 {
		uid, gid, apply = base+uid, base+gid, true
	}
	return uid, gid, apply, nil
}

func parseUIDGID(s string) (int, int, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected \"uid:gid\"")
	}
	uid, err := strconv.Atoi(parts[0])
	if err != nil || uid < 0 {
		return 0, 0, fmt.Errorf("uid not a non-negative int")
	}
	gid, err := strconv.Atoi(parts[1])
	if err != nil || gid < 0 {
		return 0, 0, fmt.Errorf("gid not a non-negative int")
	}
	return uid, gid, nil
}

// lchownTree recursively lchowns every entry under root to uid:gid. It uses
// os.Lchown (not os.Chown) so a symlink entry has the LINK re-owned, never its
// target — a container-writable volume cannot redirect a root-side chown to a
// host path via a planted symlink (Gitea #479). filepath.Walk does not descend
// into symlinked directories, so traversal stays inside the volume tree.
func lchownTree(root string, uid, gid int) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, _ error) error {
		if info == nil {
			return nil
		}
		return os.Lchown(path, uid, gid)
	})
}
