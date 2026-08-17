// docker_lifecycle.go — opt-in install / disable verbs for the M48
// docker app marketplace. Mirrors panel-agent/internal/commands/
// db_postgres_lifecycle.go pattern:
//
//   docker.install — sources install.sh and runs install_docker_engine.
//                    Idempotent; safe to re-run after a failed flip.
//   docker.disable — systemctl disable --now docker; data under
//                    /var/lib/jabali/docker-apps/ left intact for a
//                    future re-enable.
//   docker.status  — installed (binary present) + active (daemon up)
//                    + version string.
//
// Called by panel-api when the operator flips
// server_settings.docker_marketplace_enabled in Server Settings.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type dockerStatusResponse struct {
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Version   string `json:"version,omitempty"`
}

func dockerStatusHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	resp := dockerStatusResponse{}
	if _, err := exec.LookPath("docker"); err == nil {
		resp.Installed = true
	}
	if out, err := execCommandContext(ctx, "systemctl", "is-active", "docker").Output(); err == nil {
		resp.Active = strings.TrimSpace(string(out)) == "active"
	}
	if resp.Installed {
		if out, err := execCommandContext(ctx, "docker", "--version").Output(); err == nil {
			resp.Version = strings.TrimSpace(string(out))
		}
	}
	return resp, nil
}

// dockerInstallHandler sources install.sh and runs the existing
// install_docker_engine function. install.sh's docker block is
// idempotent (checks for `docker` + the compose-plugin package
// before pulling apt) so re-runs after a partial failure are safe.
// We wrap in systemd-run --pipe to escape the jabali-agent mount
// namespace (PrivateTmp + ProtectKernel* break apt + dpkg + docker
// postinst the same way they break the postgres install — same
// rationale as db.postgres.install).
func dockerInstallHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	if _, err := os.Stat(installShPath); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("install.sh missing at %s", installShPath),
		}
	}
	cmd := execCommandContext(ctx, "systemd-run",
		"--pipe", "--wait", "--quiet", "--collect",
		"--unit=jabali-docker-install",
		"--service-type=oneshot",
		"--", "bash", "-c",
		"source "+installShPath+" && install_docker_engine && "+
			"systemctl enable docker >/dev/null 2>&1 && "+
			"systemctl start docker")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("install_docker_engine failed: %v: %s",
				err, strings.TrimSpace(string(out))),
		}
	}
	return dockerStatusHandler(ctx, nil)
}

// dockerDisableHandler stops the docker daemon and masks the unit
// so it does not auto-restart on next boot. Data under
// /var/lib/jabali/docker-apps/ is intentionally left intact -- the
// operator may re-enable later. Per-app rows + volumes survive.
func dockerDisableHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	for _, args := range [][]string{
		{"systemctl", "disable", "--now", "docker"},
		{"systemctl", "disable", "--now", "docker.socket"},
	} {
		// Fail-soft: a unit that was never installed returns exit 1
		// from systemctl; treat that as success.
		_ = execCommandContext(ctx, args[0], args[1:]...).Run()
	}
	return dockerStatusHandler(ctx, nil)
}

func init() {
	Default.Register("docker.install", dockerInstallHandler)
	Default.Register("docker.disable", dockerDisableHandler)
	Default.Register("docker.status", dockerStatusHandler)
	Default.Register("docker.disk_usage", dockerDiskUsageHandler)
	Default.Register("docker.prune", dockerPruneHandler)
}

// docker.disk_usage returns the docker daemon's view of how much
// disk the marketplace is consuming. Maps to `docker system df`.
type dockerDiskUsageResponse struct {
	ImagesBytes      int64 `json:"images_bytes"`
	ContainersBytes  int64 `json:"containers_bytes"`
	VolumesBytes     int64 `json:"volumes_bytes"`
	BuildCacheBytes  int64 `json:"build_cache_bytes"`
	ReclaimableBytes int64 `json:"reclaimable_bytes"`
}

func dockerDiskUsageHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	// `docker system df --format` JSON only landed in 24.x; for
	// portability parse the plain text. Columns are stable across
	// docker versions we care about.
	out, err := execCommandContext(ctx, "docker", "system", "df").Output()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("docker system df: %v", err)}
	}
	resp := dockerDiskUsageResponse{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		// type column may be "Images" / "Containers" / "Local Volumes" /
		// "Build Cache". Pull the SIZE + RECLAIMABLE columns -- the
		// last two human-readable bytes.
		switch fields[0] {
		case "Images":
			resp.ImagesBytes = parseHumanBytes(fields[len(fields)-3])
			resp.ReclaimableBytes += parseHumanBytes(fields[len(fields)-2])
		case "Containers":
			resp.ContainersBytes = parseHumanBytes(fields[len(fields)-3])
			resp.ReclaimableBytes += parseHumanBytes(fields[len(fields)-2])
		case "Local":
			// "Local Volumes"
			if len(fields) > 1 && fields[1] == "Volumes" {
				resp.VolumesBytes = parseHumanBytes(fields[len(fields)-3])
				resp.ReclaimableBytes += parseHumanBytes(fields[len(fields)-2])
			}
		case "Build":
			if len(fields) > 1 && fields[1] == "Cache" {
				resp.BuildCacheBytes = parseHumanBytes(fields[len(fields)-3])
				resp.ReclaimableBytes += parseHumanBytes(fields[len(fields)-2])
			}
		}
	}
	return resp, nil
}

// docker.prune runs `docker system prune` with the operator-chosen
// scope. Defaults to images + dangling build cache + stopped
// containers; volumes are opt-in because they can hold real data.
type dockerPruneParams struct {
	Volumes bool `json:"volumes,omitempty"`
	All     bool `json:"all,omitempty"` // -a: also unused images, not just dangling
}

type dockerPruneResponse struct {
	ReclaimedBytes int64  `json:"reclaimed_bytes"`
	Raw            string `json:"raw"`
}

func dockerPruneHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerPruneParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
		}
	}
	args := []string{"system", "prune", "-f"}
	if p.All {
		args = append(args, "-a")
	}
	if p.Volumes {
		args = append(args, "--volumes")
	}
	out, err := execCommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("docker system prune: %v: %s", err, strings.TrimSpace(string(out)))}
	}
	reclaim := parseReclaimedSpace(string(out))
	return dockerPruneResponse{ReclaimedBytes: reclaim, Raw: strings.TrimSpace(string(out))}, nil
}

// parseReclaimedSpace pulls the byte count out of docker's
// "Total reclaimed space: 3.53GB" trailer. The value is everything after the
// colon (e.g. "3.53GB", or "3.53 GB" on some docker builds); spaces are
// stripped so parseHumanBytes sees a single size token. Returns 0 when the
// line is absent. The previous code joined the last two whitespace fields
// ("space:" + "3.53GB" -> "space:3.53GB"), which parseHumanBytes can't read,
// so every prune reported "Reclaimed 0 B" even when it freed gigabytes.
func parseReclaimedSpace(out string) int64 {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Total reclaimed space:") {
			val := strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			return parseHumanBytes(strings.ReplaceAll(val, " ", ""))
		}
	}
	return 0
}

// parseHumanBytes turns "1.234GB" / "512.5MB" / "0B" into bytes.
// Used only for docker's df/prune output where the format is stable.
func parseHumanBytes(in string) int64 {
	in = strings.TrimSpace(in)
	if in == "" || in == "-" {
		return 0
	}
	var unit string
	var numStr string
	for i, r := range in {
		if (r < '0' || r > '9') && r != '.' {
			numStr = in[:i]
			unit = strings.ToUpper(in[i:])
			break
		}
	}
	if numStr == "" {
		numStr = in
	}
	var n float64
	fmt.Sscanf(numStr, "%f", &n)
	switch unit {
	case "B":
		return int64(n)
	case "KB":
		return int64(n * 1000)
	case "MB":
		return int64(n * 1000 * 1000)
	case "GB":
		return int64(n * 1000 * 1000 * 1000)
	case "TB":
		return int64(n * 1000 * 1000 * 1000 * 1000)
	}
	return int64(n)
}
