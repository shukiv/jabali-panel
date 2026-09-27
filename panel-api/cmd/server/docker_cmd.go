package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// M49 Phase 2 (ADR-0117, GH #170). `jabali docker enable-tenant` turns a host
// into a tenant-docker host: enable daemon-wide userns-remap (so a container-
// root escape lands as the unprivileged dockremap subuid, never host root),
// retrofit existing admin-app data ownership into the remap range, and only on
// full success write the gate flag that lets max_docker_apps>0 take effect.
//
// The dangerous, irreversible-ish step (daemon-wide remap) is wrapped in a
// down -> remap -> chown -> up -> health sequence with the flag written LAST,
// so a host that fails halfway stays "tenant docker off" rather than
// half-migrated. Proven manually on mx 2026-06-21.

const (
	dockerDaemonJSON     = "/etc/docker/daemon.json"
	dockerTenantFlagPath = "/etc/jabali/docker-tenant-enabled"
	dockerSubuidPath     = "/etc/subuid"
	dockerAppsRoot       = "/var/lib/jabali/docker-apps"
	dockremapUser        = "dockremap"
	dockerCanaryImage    = "hello-world" // throwaway image for the runtime start check
)

func newDockerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docker",
		Short: "Manage the docker engine + app-marketplace host (M48/M49)",
	}
	// Single `docker` tree. Previously `newDockerEngineCmd()` registered a
	// SECOND root command with the same Use:"docker", so cobra dispatched only
	// one and the engine subcommands (enable/disable/status) were unreachable
	// (gap-audit 2026-06-22). Fold them in here.
	cmd.AddCommand(
		newDockerEngineActionCmd("enable", "Install docker engine + flip Server Settings toggle"),
		newDockerEngineActionCmd("disable", "Disable the marketplace toggle (keeps docker installed)"),
		newDockerEngineStatusCmd(),
		newDockerEnableTenantCmd(),
	)
	return cmd
}

func newDockerEnableTenantCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "enable-tenant",
		Short: "Enable userns-remap + tenant docker apps on this host (M49, GH #170)",
		Long: `Enables tenant-level Docker apps on this host:

  1. enable daemon-wide userns-remap in ` + dockerDaemonJSON + `
  2. restart dockerd
  3. chown every installed app's data tree into the dockremap subuid range
  4. bring every app back up and health-check it
  5. write ` + dockerTenantFlagPath + ` (the flag that ungates max_docker_apps)

If any app fails to come back up, the flag is NOT written: the host stays
"tenant docker off". userns-remap is daemon-wide and changes docker's storage
root — existing pulled images become invisible until re-pulled.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDockerEnableTenant(yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "proceed without the interactive confirmation")
	return cmd
}

// mergeUsernsRemap adds "userns-remap":"default" to a daemon.json document if
// absent. Returns the re-serialised JSON and whether a change was made. Pure
// (operates on bytes) so it is unit-tested without touching /etc/docker.
func mergeUsernsRemap(daemonJSON []byte) ([]byte, bool, error) {
	doc := map[string]any{}
	if len(strings.TrimSpace(string(daemonJSON))) > 0 {
		if err := json.Unmarshal(daemonJSON, &doc); err != nil {
			return nil, false, fmt.Errorf("parse daemon.json: %w", err)
		}
	}
	if v, ok := doc["userns-remap"]; ok && v == "default" {
		out, _ := json.MarshalIndent(doc, "", "  ")
		return out, false, nil
	}
	doc["userns-remap"] = "default"
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), true, nil
}

// removeUsernsRemap strips "userns-remap" from a daemon.json document. Returns
// the new bytes + whether anything was removed. Used to recover dockerd when a
// userns-remap restart fails (GH #272) — regardless of which run added it.
func removeUsernsRemap(daemonJSON []byte) ([]byte, bool, error) {
	doc := map[string]any{}
	if len(daemonJSON) > 0 {
		if err := json.Unmarshal(daemonJSON, &doc); err != nil {
			return nil, false, fmt.Errorf("parse daemon.json: %w", err)
		}
	}
	if _, ok := doc["userns-remap"]; !ok {
		return daemonJSON, false, nil
	}
	delete(doc, "userns-remap")
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), true, nil
}

// parseDockremapBase reads the base subuid for the dockremap user out of an
// /etc/subuid body (lines "name:base:count"). The chown retrofit shifts app
// data into <base>:<base>. Pure for testing.
func parseDockremapBase(subuidBody string) (int, error) {
	sc := bufio.NewScanner(strings.NewReader(subuidBody))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) >= 2 && parts[0] == dockremapUser {
			base, err := strconv.Atoi(parts[1])
			if err != nil {
				return 0, fmt.Errorf("dockremap subuid base %q not an int: %w", parts[1], err)
			}
			return base, nil
		}
	}
	return 0, fmt.Errorf("no %s entry in subuid", dockremapUser)
}

// listInstalledAppDirs returns the per-app data dirs under dockerAppsRoot (each
// holds a compose.yml). Used to down/chown/up every app during the retrofit.
func listInstalledAppDirs(root string) ([]string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var dirs []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(d, "compose.yml")); err == nil {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
}

// isLikelyUnprivilegedContainer reports whether this host is an unprivileged
// LXC/container, where docker's userns-remap cannot work. In such a container
// uid 0 is mapped to a high host uid, so /proc/self/uid_map is NOT the identity
// map "0 0 4294967295".
func isLikelyUnprivilegedContainer() bool {
	b, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return false // can't tell; let the restart+rollback path catch failures.
	}
	return uidMapIsUnprivileged(string(b))
}

// uidMapIsUnprivileged reports whether a /proc/<pid>/uid_map body describes an
// unprivileged user namespace. The identity map "0 0 4294967295" means uid 0 is
// the real root (privileged host/VM/privileged LXC). Any other 3-field mapping
// (e.g. "0 100000 65536") means root is remapped — an unprivileged container,
// where docker userns-remap can't work. Unparseable -> false (let rollback catch).
func uidMapIsUnprivileged(uidMap string) bool {
	f := strings.Fields(strings.TrimSpace(uidMap))
	if len(f) != 3 {
		return false
	}
	return !(f[0] == "0" && f[1] == "0")
}

// dockerRuntimeCanary starts a throwaway container to confirm the runtime can
// actually create containers under the active userns-remap. nil = ok OR the
// check was inconclusive (canary image couldn't be fetched — a transient/offline
// condition that must not block enable). A non-nil error means a container
// provably failed to start, with a specific hint for the nested-LXC runc/
// AppArmor case (GH #446).
func dockerRuntimeCanary() error {
	out, err := exec.Command("docker", "run", "--rm", dockerCanaryImage).CombinedOutput()
	res := classifyCanaryOutput(string(out), err)
	if res == nil && err != nil {
		fmt.Fprintf(os.Stderr, "warning: tenant docker runtime check inconclusive (could not run %s): %s\n",
			dockerCanaryImage, lastChars(strings.TrimSpace(string(out)), 200))
	}
	return res
}

// classifyCanaryOutput is the pure decision for dockerRuntimeCanary: given the
// combined output + run error of the canary container, return nil to proceed
// (success or an inconclusive image-fetch failure) or a diagnostic error to
// block. Separated for unit testing.
func classifyCanaryOutput(out string, runErr error) error {
	if runErr == nil {
		return nil
	}
	s := strings.TrimSpace(out)
	low := strings.ToLower(s)
	// Inconclusive: the canary image couldn't be fetched (offline / registry).
	if strings.Contains(low, "manifest") || strings.Contains(low, "pull access") ||
		strings.Contains(low, "no such host") || strings.Contains(low, "i/o timeout") ||
		strings.Contains(low, "connection refused") || strings.Contains(low, "timeout exceeded") {
		return nil
	}
	if strings.Contains(s, "ip_unprivileged_port_start") ||
		(strings.Contains(low, "permission denied") && strings.Contains(low, "sysctl")) {
		return fmt.Errorf("a test container could not start:\n  %s\n\nThis host looks like a nested LXC/Incus/Proxmox container: runc 1.3.x's security fix is denied by the container's AppArmor profile, so NO container can start (a bare `docker run hello-world` fails too). Patch the host's container AppArmor profile (Incus/Proxmox update) or run tenant docker on a VM / bare-metal. Base docker is unaffected. (GH #446)", lastChars(s, 400))
	}
	return fmt.Errorf("a test container could not start:\n  %s", lastChars(s, 400))
}

// revertUsernsRemapBestEffort strips userns-remap from daemon.json + restarts
// dockerd so base docker is fully restored. Best-effort; used when a post-
// restart step fails before any app data has been migrated.
func revertUsernsRemapBestEffort() {
	onDisk, err := os.ReadFile(dockerDaemonJSON)
	if err != nil {
		return
	}
	cleaned, removed, cerr := removeUsernsRemap(onDisk)
	if cerr != nil || !removed {
		return
	}
	if len(cleaned) <= 3 { // "{}\n" — nothing worth keeping
		_ = os.Remove(dockerDaemonJSON)
	} else {
		_ = os.WriteFile(dockerDaemonJSON, cleaned, 0o644)
	}
	_ = exec.Command("systemctl", "reset-failed", "docker").Run()
	_ = exec.Command("systemctl", "restart", "docker").Run()
}

// lastChars returns the trailing n chars of s (whole string if shorter).
func lastChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func runDockerEnableTenant(yes bool) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root")
	}
	if isLikelyUnprivilegedContainer() {
		return fmt.Errorf("tenant docker needs docker userns-remap, which requires user-namespace support not available in this unprivileged LXC/container. Run on a VM, or a privileged LXC with nesting enabled + a subuid/subgid map. (Base docker is unaffected.)")
	}
	if _, err := os.Stat(dockerTenantFlagPath); err == nil {
		fmt.Println("tenant docker already enabled (" + dockerTenantFlagPath + " exists)")
		return nil
	}
	apps, err := listInstalledAppDirs(dockerAppsRoot)
	if err != nil {
		return fmt.Errorf("scan installed apps: %w", err)
	}
	if !yes {
		fmt.Printf("This enables daemon-wide userns-remap and retrofits %d installed app(s).\n", len(apps))
		fmt.Printf("Existing pulled images become invisible until re-pulled. Re-run with --yes to proceed.\n")
		return nil
	}

	// 1. down every app.
	for _, d := range apps {
		_ = dockerComposeAt(d, "down").Run()
	}

	// 2. enable remap in daemon.json + restart dockerd.
	cur, _ := os.ReadFile(dockerDaemonJSON)
	merged, changed, err := mergeUsernsRemap(cur)
	if err != nil {
		return err
	}
	if changed {
		if err := os.WriteFile(dockerDaemonJSON, merged, 0o644); err != nil {
			return fmt.Errorf("write daemon.json: %w", err)
		}
	}
	if out, err := exec.Command("systemctl", "restart", "docker").CombinedOutput(); err != nil {
		// userns-remap broke dockerd. Most common cause: an unprivileged LXC,
		// where nested user namespaces for docker are unavailable. REVERT
		// daemon.json + restart so the base docker daemon comes back instead of
		// being left permanently dead (GH #272), then report a clear error.
		// ALWAYS strip userns-remap + restart, regardless of whether THIS run
		// added it — the goal is that docker comes back, and userns-remap is the
		// only thing this command manages. Re-read the file so we clean whatever
		// is actually on disk.
		if onDisk, rerr := os.ReadFile(dockerDaemonJSON); rerr == nil {
			if cleaned, removed, cerr := removeUsernsRemap(onDisk); cerr == nil && removed {
				if len(cleaned) <= 3 { // "{}\n" — nothing left worth keeping
					_ = os.Remove(dockerDaemonJSON)
				} else {
					_ = os.WriteFile(dockerDaemonJSON, cleaned, 0o644)
				}
				_ = exec.Command("systemctl", "reset-failed", "docker").Run()
				_ = exec.Command("systemctl", "restart", "docker").Run()
			}
		}
		hint := ""
		if isLikelyUnprivilegedContainer() {
			hint = " This host is an unprivileged LXC/container — docker userns-remap is unsupported here. Run tenant docker on a VM, or a privileged LXC with nesting + a subuid/subgid map."
		}
		return fmt.Errorf("restart docker with userns-remap failed (reverted; base docker restored): %v: %s.%s", err, strings.TrimSpace(string(out)), hint)
	}

	// 2b. Runtime canary — prove a container can actually START under the new
	// userns-remap before touching app data or writing the gate flag. The userns
	// checks above pass on a nested LXC/Incus/Proxmox host, but runc 1.3.x's
	// CVE-2025-52881 fix is then denied by the LXC AppArmor profile and EVERY
	// container fails to init; without this, every future tenant install would
	// silently strand as "failed" (GH #284 / #446).
	if cerr := dockerRuntimeCanary(); cerr != nil {
		revertUsernsRemapBestEffort() // no app data touched yet — clean rollback
		return fmt.Errorf("tenant docker runtime check failed (reverted; base docker restored): %w", cerr)
	}

	// 3. chown app data trees into the dockremap range.
	subuid, _ := os.ReadFile(dockerSubuidPath)
	base, err := parseDockremapBase(string(subuid))
	if err != nil {
		return fmt.Errorf("resolve dockremap base (remap not active?): %w", err)
	}
	for _, d := range apps {
		if out, err := exec.Command("chown", "-R", fmt.Sprintf("%d:%d", base, base), d).CombinedOutput(); err != nil {
			return fmt.Errorf("chown %s into remap range: %v: %s", d, err, out)
		}
	}

	// 4. bring every app back up + health.
	for _, d := range apps {
		if out, err := dockerComposeAt(d, "up", "-d").CombinedOutput(); err != nil {
			return fmt.Errorf("app %s failed to come back up post-remap (flag NOT written): %v: %s", filepath.Base(d), err, out)
		}
	}

	// 5. success: write the gate flag.
	if err := os.MkdirAll(filepath.Dir(dockerTenantFlagPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dockerTenantFlagPath, []byte("M49 tenant docker enabled\n"), 0o644); err != nil {
		return fmt.Errorf("write tenant flag: %w", err)
	}
	fmt.Printf("tenant docker enabled: userns-remap active, %d app(s) retrofitted, %s written\n", len(apps), dockerTenantFlagPath)
	return nil
}

// dockerComposeAt builds `docker compose -f <dir>/compose.yml <args...>` with
// COMPOSE_DISABLE_ENV_FILE set, matching the agent's composeCommand: the app's
// .env is jabali's own store of its env values, no catalog template
// interpolates from it, and compose's dotenv parser rejects some values that
// start with a quote, which would abort the retrofit mid-way.
func dockerComposeAt(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("docker", append([]string{"compose", "-f", filepath.Join(dir, "compose.yml")}, args...)...)
	cmd.Env = append(os.Environ(), "COMPOSE_DISABLE_ENV_FILE=true")
	return cmd
}
