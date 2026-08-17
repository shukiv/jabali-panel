package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// M49 (GH #170) — tenant-install safety gate. Even though panel-api injects the
// hardening profile into the rendered compose, the agent independently
// validates the FULLY-RESOLVED config (`docker compose config`) before bringing
// a tenant app up. Defense in depth: a buggy/compromised panel cannot get a
// privileged container, an out-of-allowlist capability, or a host bind-mount
// past the agent. The catalog templates are admin/tenant-neutral, so this gate
// is the load-bearing check that a tenant install is actually contained.

// composeConfigService is the slice of `docker compose config --format json`
// we care about. Unknown fields are ignored.
type composeConfigService struct {
	Privileged bool     `json:"privileged"`
	CapAdd     []string `json:"cap_add"`
	// Host-namespace / device escapes (GH #450). docker compose config
	// normalizes these to scalars/arrays; extends is already resolved away.
	NetworkMode       string          `json:"network_mode"`
	Pid               string          `json:"pid"`
	Ipc               string          `json:"ipc"`
	Uts               string          `json:"uts"`
	UsernsMode        string          `json:"userns_mode"`
	Devices           []any           `json:"devices"`
	DeviceCgroupRules []string        `json:"device_cgroup_rules"`
	SecurityOpt       []string        `json:"security_opt"`
	VolumesFrom       []string        `json:"volumes_from"`
	CapDrop           []string        `json:"cap_drop"`
	CgroupParent      string          `json:"cgroup_parent"`
	Build             json.RawMessage `json:"build"`
	EnvFile           json.RawMessage `json:"env_file"`
	Secrets           []any           `json:"secrets"`
	Configs           []any           `json:"configs"`
	Volumes           []struct {
		Type   string `json:"type"`
		Source string `json:"source"`
		Target string `json:"target"`
	} `json:"volumes"`
	// Resolved published ports. `docker compose config --format json` renders
	// each as an object with host_ip/published/target/protocol.
	Ports []struct {
		Mode      string `json:"mode"`
		HostIP    string `json:"host_ip"`
		Published string `json:"published"`
		Target    int    `json:"target"`
		Protocol  string `json:"protocol"`
	} `json:"ports"`
}

type composeConfigDoc struct {
	Services map[string]composeConfigService `json:"services"`
	Secrets  map[string]any                  `json:"secrets"`
	Configs  map[string]any                  `json:"configs"`
}

// normalizeCap upper-cases and strips a leading CAP_ so "cap_chown",
// "CHOWN" and "CAP_CHOWN" all compare equal.
func normalizeCap(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	return strings.TrimPrefix(c, "CAP_")
}

// validateTenantCompose rejects a resolved compose config that a tenant must
// never be able to run: any privileged service, any cap_add outside the
// catalog-verified allowlist, or any host bind-mount whose source escapes the
// app's own data tree (dataRoot). Pure (operates on the JSON bytes) so it is
// unit-tested without docker. Returns nil when the config is safe.
// isForbiddenNamespace reports whether a namespace-mode value escapes the
// tenant sandbox: "host" (share the host namespace) or "container:<id>" (join
// another container's). Empty / "none" / "private" / a compose service ref are
// fine. (GH #450)
func isForbiddenNamespace(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "host" || strings.HasPrefix(v, "container:")
}

// normalizeSecurityOpt lowercases + trims and rewrites the "key=value" form to
// the "key:value" form docker compose config emits, so the no-new-privileges
// allow-check is format-insensitive. (GH #450)
func normalizeSecurityOpt(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexByte(s, '='); i != -1 {
		s = s[:i] + ":" + s[i+1:]
	}
	return s
}

// tenantCgroupRE matches the per-tenant M18 slice the render sets as
// cgroup_parent, e.g. "jabali-user-bob.slice".
var tenantCgroupRE = regexp.MustCompile(`^jabali-user-[A-Za-z0-9._-]+\.slice$`)

func validateTenantCompose(configJSON []byte, allowedCaps []string, dataRoot, expectedCgroup string) error {
	var doc composeConfigDoc
	if err := json.Unmarshal(configJSON, &doc); err != nil {
		return fmt.Errorf("parse compose config: %w", err)
	}
	if len(doc.Services) == 0 {
		return fmt.Errorf("compose config has no services")
	}
	// Top-level secrets/configs expose host files into containers (#518).
	if len(doc.Secrets) > 0 {
		return fmt.Errorf("top-level secrets are forbidden for tenant installs")
	}
	if len(doc.Configs) > 0 {
		return fmt.Errorf("top-level configs are forbidden for tenant installs")
	}
	allow := make(map[string]bool, len(allowedCaps))
	for _, c := range allowedCaps {
		allow[normalizeCap(c)] = true
	}
	root := filepath.Clean(dataRoot)
	for name, svc := range doc.Services {
		if svc.Privileged {
			return fmt.Errorf("service %q requests privileged: forbidden for tenant installs", name)
		}
		for _, c := range svc.CapAdd {
			if !allow[normalizeCap(c)] {
				return fmt.Errorf("service %q requests capability %q outside the tenant allowlist", name, c)
			}
		}
		// Host-namespace escapes: joining the host (or another container's)
		// network/pid/ipc/uts/user namespace defeats tenant isolation (#450).
		for field, val := range map[string]string{
			"network_mode": svc.NetworkMode,
			"pid":          svc.Pid,
			"ipc":          svc.Ipc,
			"uts":          svc.Uts,
			"userns_mode":  svc.UsernsMode,
		} {
			if isForbiddenNamespace(val) {
				return fmt.Errorf("service %q sets %s=%q (host/other-container namespace): forbidden for tenant installs", name, field, val)
			}
		}
		// Direct device access bypasses cgroup device isolation.
		if len(svc.Devices) > 0 {
			return fmt.Errorf("service %q requests devices: forbidden for tenant installs", name)
		}
		if len(svc.DeviceCgroupRules) > 0 {
			return fmt.Errorf("service %q sets device_cgroup_rules: forbidden for tenant installs", name)
		}
		// volumes_from attaches another container's volumes, outside the app
		// data tree and the bind-mount containment check above.
		if len(svc.VolumesFrom) > 0 {
			return fmt.Errorf("service %q uses volumes_from: forbidden for tenant installs", name)
		}
		// security_opt: only the injected no-new-privileges:true is allowed;
		// anything else (seccomp/apparmor unconfined, label:disable, ...) would
		// loosen the sandbox the panel just hardened.
		for _, so := range svc.SecurityOpt {
			if normalizeSecurityOpt(so) != "no-new-privileges:true" {
				return fmt.Errorf("service %q sets security_opt %q outside the allowed no-new-privileges:true: forbidden for tenant installs", name, so)
			}
		}
		// #516: the tenant hardening profile must actually be PRESENT on every
		// service — the render injects it, so its absence means an unhardened
		// compose slipped in. Require cap_drop: ALL and no-new-privileges:true.
		hasCapDropAll := false
		for _, c := range svc.CapDrop {
			if normalizeCap(c) == "ALL" {
				hasCapDropAll = true
			}
		}
		if !hasCapDropAll {
			return fmt.Errorf("service %q is missing cap_drop: ALL (tenant hardening absent): forbidden for tenant installs", name)
		}
		hasNoNewPriv := false
		for _, so := range svc.SecurityOpt {
			if normalizeSecurityOpt(so) == "no-new-privileges:true" {
				hasNoNewPriv = true
			}
		}
		if !hasNoNewPriv {
			return fmt.Errorf("service %q is missing security_opt no-new-privileges:true (tenant hardening absent): forbidden for tenant installs", name)
		}
		// cgroup_parent must place the container under the owner's M18 tenant
		// slice (Gitea #516) — it is the load-bearing isolation the render
		// injects (it gates the M18 cpu/mem limits, the #519 loopback-port
		// isolation, and the M34 egress firewall). Require it as defense-in-depth
		// against a render/re-render path dropping it.
		// Gitea #525: when the panel passes the expected owner slice, require an
		// EXACT match — a generic-pattern match would let a tenant compose declare
		// ANOTHER tenant's slice and pass, applying M18 limits / loopback / egress
		// isolation + audit attribution to the wrong owner.
		if expectedCgroup != "" {
			if svc.CgroupParent != expectedCgroup {
				return fmt.Errorf("service %q cgroup_parent %q does not match the owner slice %q (cross-tenant or missing hardening): forbidden for tenant installs", name, svc.CgroupParent, expectedCgroup)
			}
		} else if !tenantCgroupRE.MatchString(svc.CgroupParent) {
			return fmt.Errorf("service %q cgroup_parent %q is not a jabali tenant slice (tenant hardening absent): forbidden for tenant installs", name, svc.CgroupParent)
		}
		// #518: build runs arbitrary build-time code; env_file/secrets/configs
		// read host files into the container outside the bind-mount check.
		if len(svc.Build) > 0 && string(svc.Build) != "null" {
			return fmt.Errorf("service %q sets build: forbidden for tenant installs", name)
		}
		if len(svc.EnvFile) > 0 && string(svc.EnvFile) != "null" {
			return fmt.Errorf("service %q sets env_file: forbidden for tenant installs", name)
		}
		if len(svc.Secrets) > 0 {
			return fmt.Errorf("service %q uses secrets: forbidden for tenant installs", name)
		}
		if len(svc.Configs) > 0 {
			return fmt.Errorf("service %q uses configs: forbidden for tenant installs", name)
		}
		for _, v := range svc.Volumes {
			// #514: tenant data must live under the bind-mounted app data tree
			// so disk accounting + backups cover it. Named/anonymous volumes
			// live in docker's volume store outside that tree — reject them.
			if v.Type != "bind" {
				return fmt.Errorf("service %q uses a %q volume (target %q): only bind mounts under the app data tree are allowed for tenant installs", name, v.Type, v.Target)
			}
			src := filepath.Clean(v.Source)
			if src != root && !strings.HasPrefix(src, root+string(filepath.Separator)) {
				return fmt.Errorf("service %q bind-mounts %q outside the app data tree %q: forbidden for tenant installs", name, v.Source, root)
			}
			// Gitea #531: a lexical prefix check accepts a source that is (or
			// passes through) a symlink resolving outside the data tree. Resolve
			// symlinks on the longest existing prefix and re-check containment.
			if pathEscapesRoot(v.Source, dataRoot) {
				return fmt.Errorf("service %q bind-mounts %q which resolves (via symlink) outside the app data tree %q: forbidden for tenant installs", name, v.Source, root)
			}
		}
		// Published ports MUST bind loopback only (#508). The catalog filter +
		// panel resolver already enforce loopback, but a template regression or
		// a bad re-render could publish 0.0.0.0:<port>; the agent is the trust
		// boundary, so reject any published port whose host_ip is not 127.0.0.1
		// / ::1 (empty host_ip means docker binds 0.0.0.0 — also rejected).
		for _, p := range svc.Ports {
			if p.Published == "" {
				continue // not host-published (internal only) — fine
			}
			if !isLoopbackHostIP(p.HostIP) {
				return fmt.Errorf("service %q publishes port %s on host_ip %q (not loopback): forbidden for tenant installs", name, p.Published, p.HostIP)
			}
		}
	}
	return nil
}

// isLoopbackHostIP reports whether a docker-published host_ip is loopback.
// Empty (docker defaults to 0.0.0.0) and any non-loopback address are NOT.
func isLoopbackHostIP(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1"
}

// runTenantComposeValidation resolves the on-disk compose (dir) via
// `docker compose config --format json` and runs validateTenantCompose.
// Called by the install handler before `up` when the install is tenant-owned.
func runTenantComposeValidation(ctx context.Context, dir string, allowedCaps []string, expectedCgroup string) error {
	cmd := execCommandContext(ctx, "docker", "compose", "config", "--format", "json")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose config failed: %v: %s", err, lastNonEmptyLines(string(out), 5))
	}
	return validateTenantCompose(out, allowedCaps, dir, expectedCgroup)
}

// realPath resolves symlinks on the longest existing prefix of p and rejoins the
// non-existent tail (bind sources may not exist until `up`). Clean fallback to
// the lexical path when nothing resolves.
func realPath(p string) string {
	p = filepath.Clean(p)
	cur := p
	rel := ""
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if rel == "" {
				return resolved
			}
			return filepath.Join(resolved, rel)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rel = filepath.Join(filepath.Base(cur), rel)
		cur = parent
	}
}

// pathEscapesRoot reports whether src, after symlink resolution, falls outside
// root (also symlink-resolved). Used to reject tenant bind mounts that escape
// the app data tree through a symlink (Gitea #531).
func pathEscapesRoot(src, root string) bool {
	rroot := realPath(filepath.Clean(root))
	rsrc := realPath(src)
	return rsrc != rroot && !strings.HasPrefix(rsrc, rroot+string(filepath.Separator))
}
