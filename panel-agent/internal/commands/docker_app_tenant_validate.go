package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/tenantcompose"
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
	// GH #1903: the networks the service joins (keys only; values are null or
	// alias settings) and its cgroup namespace mode.
	Networks map[string]json.RawMessage `json:"networks"`
	Cgroup   string                     `json:"cgroup"`
}

type composeConfigDoc struct {
	// Name is the compose project. It decides which containers and networks
	// `up` and `down` act on (GH #1903).
	Name     string                          `json:"name"`
	Services map[string]composeConfigService `json:"services"`
	Networks map[string]composeNetwork       `json:"networks"`
	Secrets  map[string]any                  `json:"secrets"`
	Configs  map[string]any                  `json:"configs"`
}

// composeNetwork is a resolved top-level network definition.
type composeNetwork struct {
	Name       string          `json:"name"`
	External   bool            `json:"external"`
	Driver     string          `json:"driver"`
	DriverOpts json.RawMessage `json:"driver_opts"`
	Ipam       struct {
		Driver string          `json:"driver"`
		Config json.RawMessage `json:"config"`
	} `json:"ipam"`
}

// forbiddenServiceKeys are resolved service keys the tenant render never
// emits (GH #1903). Each one either reaches past the owner's slice — device
// grants (gpus), another OCI runtime, the host OOM killer's choice of victim,
// kernel parameters — or sets a limit outside deploy.resources.limits, where
// the service-set pin would not see it. pids_limit is not here: the M49
// hardening wrote it before GH #284, so an older on-disk compose may still
// carry it; the service-set pin rejects it on every fresh render.
var forbiddenServiceKeys = []string{
	"gpus", "runtime", "oom_score_adj", "oom_kill_disable", "sysctls",
	"mem_limit", "memswap_limit", "mem_reservation", "mem_swappiness",
	"cpus", "cpu_count", "cpu_percent", "cpu_shares", "cpu_period", "cpu_quota",
	"cpu_rt_runtime", "cpu_rt_period", "cpuset",
}

// rawIsSet reports whether a resolved JSON value sets anything: not absent,
// null, false, zero, empty string, empty list or empty object.
func rawIsSet(v json.RawMessage) bool {
	switch strings.TrimSpace(string(v)) {
	case "", "null", "false", "0", `""`, "[]", "{}":
		return false
	}
	return true
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
	// GH #1903: a top-level `name:` renames the compose project, and `up` /
	// `down` then act on whatever project carries that name — another app's
	// containers. The project must be the app directory's own name, which is
	// what compose derives when the file sets none.
	project := filepath.Base(filepath.Clean(dataRoot))
	if doc.Name != project {
		return fmt.Errorf("compose project name %q is not the app's own %q: forbidden for tenant installs", doc.Name, project)
	}
	if err := checkTenantNetworks(doc, project); err != nil {
		return err
	}
	var rawDoc struct {
		Services map[string]map[string]json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(configJSON, &rawDoc); err != nil {
		return fmt.Errorf("parse compose config: %w", err)
	}
	for _, name := range sortedServiceNames(rawDoc.Services) {
		for _, key := range forbiddenServiceKeys {
			if rawIsSet(rawDoc.Services[name][key]) {
				return fmt.Errorf("service %q sets %s: forbidden for tenant installs", name, key)
			}
		}
		if res := nestedRaw(rawDoc.Services[name]["deploy"], "resources", "reservations"); rawIsSet(res) {
			return fmt.Errorf("service %q sets deploy.resources.reservations: forbidden for tenant installs", name)
		}
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
			"cgroup":       svc.Cgroup,
		} {
			if isForbiddenNamespace(val) {
				return fmt.Errorf("service %q sets %s=%q (host/other-container namespace): forbidden for tenant installs", name, field, val)
			}
		}
		// GH #1903: a service may only use the project's own default network.
		// network_mode "bridge" (docker's shared default bridge, where every
		// container can reach every other) or any named network would put the
		// container next to other tenants' containers.
		if err := checkTenantNetworkMode(name, svc.NetworkMode, doc.Services); err != nil {
			return err
		}
		for net := range svc.Networks {
			if net != "default" {
				return fmt.Errorf("service %q joins network %q: tenant apps may only use the project's default network", name, net)
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

// checkTenantNetworks requires the resolved top-level networks to be at most
// the project's own default network, created by compose with its own name
// and plain settings (GH #1903). An `external: true` default or extra network
// would join a network another app created; a custom name, driver option or
// subnet would do the same or reach outside docker's own address pool.
func checkTenantNetworks(doc composeConfigDoc, project string) error {
	for name, n := range doc.Networks {
		if name != "default" {
			return fmt.Errorf("compose defines network %q: tenant apps may only use the project's default network", name)
		}
		switch {
		case n.External:
			return fmt.Errorf("the default network is external (%q): tenant apps may not join another project's network", n.Name)
		case n.Name != project+"_default":
			return fmt.Errorf("the default network is named %q, not %q: forbidden for tenant installs", n.Name, project+"_default")
		case n.Driver != "" && n.Driver != "bridge":
			return fmt.Errorf("the default network uses driver %q: forbidden for tenant installs", n.Driver)
		case rawIsSet(n.DriverOpts):
			return fmt.Errorf("the default network sets driver_opts: forbidden for tenant installs")
		case n.Ipam.Driver != "" || rawIsSet(n.Ipam.Config):
			return fmt.Errorf("the default network sets its own IPAM: forbidden for tenant installs")
		}
	}
	return nil
}

// checkTenantNetworkMode allows a service no network_mode (the project's
// default network), "none", or another service of the same project.
func checkTenantNetworkMode(name, mode string, services map[string]composeConfigService) error {
	m := strings.TrimSpace(mode)
	if m == "" || m == "none" {
		return nil
	}
	if target, ok := strings.CutPrefix(m, "service:"); ok {
		if _, exists := services[target]; exists {
			return nil
		}
	}
	return fmt.Errorf("service %q sets network_mode %q: tenant apps may only use the project's default network", name, mode)
}

// validateTenantServices requires the resolved services to match the set
// panel-api rendered from the catalog template without tenant input (GH
// #1903): the same service names, images and limits. It also refuses the
// legacy top-level pids_limit, which a fresh render never emits.
func validateTenantServices(configJSON []byte, want tenantcompose.Services) error {
	var doc struct {
		Services map[string]struct {
			Image     string          `json:"image"`
			PidsLimit json.RawMessage `json:"pids_limit"`
			Deploy    struct {
				Resources struct {
					Limits map[string]json.RawMessage `json:"limits"`
				} `json:"resources"`
			} `json:"deploy"`
			ShmSize json.RawMessage            `json:"shm_size"`
			Ulimits map[string]json.RawMessage `json:"ulimits"`
		} `json:"services"`
	}
	if err := json.Unmarshal(configJSON, &doc); err != nil {
		return fmt.Errorf("parse compose config: %w", err)
	}
	got := make(tenantcompose.Services, len(doc.Services))
	for _, name := range sortedServiceNames(doc.Services) {
		svc := doc.Services[name]
		if rawIsSet(svc.PidsLimit) {
			return fmt.Errorf("service %q sets pids_limit: forbidden for tenant installs", name)
		}
		s := tenantcompose.Service{Image: svc.Image}
		var err error
		for key, v := range svc.Deploy.Resources.Limits {
			switch key {
			case "cpus":
				s.CPUs, err = resolvedFloat(v)
			case "memory":
				s.MemoryBytes, err = resolvedInt(v)
			case "pids":
				s.PIDs, err = resolvedInt(v)
			default:
				return fmt.Errorf("service %q sets deploy.resources.limits.%s: forbidden for tenant installs", name, key)
			}
			if err != nil {
				return fmt.Errorf("service %q deploy.resources.limits.%s: %w", name, key, err)
			}
		}
		if s.ShmSizeBytes, err = resolvedInt(svc.ShmSize); err != nil {
			return fmt.Errorf("service %q shm_size: %w", name, err)
		}
		for n, v := range svc.Ulimits {
			if s.Ulimits == nil {
				s.Ulimits = make(map[string]tenantcompose.Ulimit, len(svc.Ulimits))
			}
			u, err := resolvedUlimit(v)
			if err != nil {
				return fmt.Errorf("service %q ulimit %s: %w", name, n, err)
			}
			s.Ulimits[n] = u
		}
		got[name] = s
	}
	return want.Check(got)
}

// resolvedInt reads an integer docker compose config printed either as a
// number or as a decimal string (byte sizes come out as "536870912").
// Absent or null reads as zero.
func resolvedInt(v json.RawMessage) (int64, error) {
	s := strings.TrimSpace(string(v))
	if s == "" || s == "null" {
		return 0, nil
	}
	if unq, err := strconv.Unquote(s); err == nil {
		s = unq
	}
	return strconv.ParseInt(s, 10, 64)
}

// resolvedFloat reads a cpus value, printed as a number by current Compose
// and as a string ("0.5") by older v2 releases.
func resolvedFloat(v json.RawMessage) (float64, error) {
	s := strings.TrimSpace(string(v))
	if unq, err := strconv.Unquote(s); err == nil {
		s = unq
	}
	return strconv.ParseFloat(s, 64)
}

// resolvedUlimit reads a ulimit: a single number (soft = hard) or an object
// with soft and hard.
func resolvedUlimit(v json.RawMessage) (tenantcompose.Ulimit, error) {
	var pair struct {
		Soft int64 `json:"soft"`
		Hard int64 `json:"hard"`
	}
	if err := json.Unmarshal(v, &pair); err == nil {
		return tenantcompose.Ulimit{Soft: pair.Soft, Hard: pair.Hard}, nil
	}
	n, err := resolvedInt(v)
	if err != nil {
		return tenantcompose.Ulimit{}, err
	}
	return tenantcompose.Ulimit{Soft: n, Hard: n}, nil
}

// nestedRaw walks JSON objects by key and returns the value at the path, or
// nil when any step is missing or not an object.
func nestedRaw(v json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		var m map[string]json.RawMessage
		if len(v) == 0 || json.Unmarshal(v, &m) != nil {
			return nil
		}
		v = m[k]
	}
	return v
}

func sortedServiceNames[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isLoopbackHostIP reports whether a docker-published host_ip is loopback.
// Empty (docker defaults to 0.0.0.0) and any non-loopback address are NOT.
func isLoopbackHostIP(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1"
}

// runTenantComposeValidation resolves the on-disk compose (dir) via
// `docker compose config --format json` and runs validateTenantCompose.
// Called by the install handler before `up` when the install is tenant-owned.
// services is the set panel-api rendered (GH #1903); the doors that write a
// fresh compose pass it and the doors that bring up the on-disk one pass nil,
// since that file was pinned when it was written.
func runTenantComposeValidation(ctx context.Context, dir string, allowedCaps []string, expectedCgroup string, services tenantcompose.Services) error {
	out, err := composeCommand(ctx, dir, "config", "--format", "json").CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose config failed: %v: %s", err, lastNonEmptyLines(string(out), 5))
	}
	if err := validateTenantCompose(out, allowedCaps, dir, expectedCgroup); err != nil {
		return err
	}
	if services != nil {
		return validateTenantServices(out, services)
	}
	return nil
}

// errNoTenantServices refuses a tenant compose write that came without the
// expected service set: every panel-api door that renders a tenant compose
// sends it, so its absence means an older panel-api or a door that skipped the
// pin (GH #1903). Fail closed.
var errNoTenantServices = errors.New("panel-api sent no expected service set for this tenant compose; update panel-api and the agent together (GH #1903)")

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
