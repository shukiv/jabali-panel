// render.go — render a catalog Entry's compose.yml.tmpl into an
// installable docker-compose YAML. Owned by panel-api; the agent
// receives the already-rendered text + the supporting files
// (volumes + ports + env) and never touches the catalog itself.
package dockerapp

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// validComposeDomainRe is the RFC-1035 FQDN alphabet (letters, digits, dot,
// hyphen). The domain is tenant-supplied free text substituted UNESCAPED into
// compose.yml.tmpl (MAIL_DOMAIN: "{{ .Domain }}", ROOT_URL: "https://{{ .Domain
// }}/", …) in the catalog templates, none of which apply the q helper. A value
// carrying a YAML-significant byte — a quote, colon, newline, brace — would break
// out of the double-quoted scalar and inject sibling keys or an entire extra
// service into the rendered compose (GH #1790). Rejecting anything outside this
// alphabet at Render — the one chokepoint every install / update / rename / CLI
// door funnels through — closes that injection class regardless of which door
// forgot to validate its own input. Empty is allowed (a domain-less render is
// not an injection); the leading/trailing-dot and length shape is enforced by
// the api-layer validateDomainName, which this guard deliberately does not
// duplicate (importing api here would be a cycle).
var validComposeDomainRe = regexp.MustCompile(`^[A-Za-z0-9.-]*$`)

// RenderParams holds everything the compose template needs.
// Field names match the template-variable contract documented in
// install/docker-apps/README.md.
type RenderParams struct {
	Slug         string
	Name         string
	Domain       string
	ImageChannel string
	DataRoot     string
	CPULimit     string
	MemoryLimit  string
	PIDsLimit    int
	// Ports maps the catalog port name (e.g. "http", "ssh") to the
	// runtime binding chosen at install time. Disabled ports are
	// omitted from the map; templates can safely range over the map.
	Ports map[string]RuntimePort
	// Env is the materialised env-var set: catalog defaults + any
	// auto-generated secrets (e.g. ADMIN_TOKEN for vaultwarden).
	Env map[string]string
	// TenantHardening, when non-nil, applies the M49 (GH #170) tenant
	// hardening profile to every service AFTER template render: cap_drop
	// ALL + the verified cap allowlist, no-new-privileges, a pids cap, and
	// cgroup_parent nesting under the tenant's M18 slice. Nil = admin/
	// server-level install (M48 behaviour — no hardening injected).
	TenantHardening *TenantHardening
}

// TenantHardening is the per-service security profile injected into a tenant
// install's rendered compose (M49). All fields are computed by panel-api from
// the owner + catalog; never tenant-supplied.
type TenantHardening struct {
	// CgroupParent is the tenant's M18 slice, e.g.
	// jabali-user-<username>.slice, so the container nests under it and the
	// package CPU/mem caps apply (proven in the Phase 0 spike).
	CgroupParent string
	// Caps is the catalog-declared verified minimal allowlist (tenant_caps)
	// added back after cap_drop:ALL. Empty = the app needs no caps.
	Caps []string
	// PIDsLimit caps fork-bombs per service. 0 = leave unset.
	PIDsLimit int
}

// RuntimePort is the resolved binding for one published port.
// BindInterface is either "127.0.0.1" (loopback) or the operator-
// chosen managed-IP address (public bind).
type RuntimePort struct {
	HostPort      int
	ContainerPort int
	BindInterface string
	Protocol      string
}

// Render returns the docker-compose YAML for the given Entry and
// runtime parameters. Errors surface template parse failures, which
// indicate a malformed catalog entry — the agent NEVER sees an
// unrenderable template because catalog loading runs validate()
// before exposing the entry.
//
// Every render is hardened: a tenant install (TenantHardening set) gets the
// tenant profile, and an admin install gets the admin baseline.
func Render(entry Entry, params RenderParams) (string, error) {
	raw, err := renderTemplate(entry, params)
	if err != nil {
		return "", err
	}
	if params.TenantHardening != nil {
		return applyTenantHardening(raw, *params.TenantHardening)
	}
	return applyAdminHardening(raw, params.PIDsLimit)
}

// renderTemplate executes the catalog template and checks the result is
// valid YAML. It applies no hardening.
func renderTemplate(entry Entry, params RenderParams) (string, error) {
	// SECURITY (GH #1790): reject a domain carrying any byte outside the FQDN
	// alphabet before it reaches the template. The catalog templates substitute
	// {{ .Domain }} unescaped inside double-quoted YAML scalars, so a quote /
	// colon / newline would break out and inject compose keys. This is the single
	// chokepoint every render door (HTTP install + update + rename, and the CLI)
	// flows through, so it holds even for a door that skipped its own validation.
	if !validComposeDomainRe.MatchString(params.Domain) {
		return "", fmt.Errorf("invalid domain %q: only letters, digits, '.' and '-' are allowed", params.Domain)
	}
	// q renders a string as a YAML-safe double-quoted scalar (JSON encoding is a
	// valid YAML scalar), so operator-supplied values with quotes/colons/#/etc.
	// (e.g. SMTP API-key passwords) can't break the rendered compose (GH #322).
	tmpl, err := template.New(entry.Slug).Funcs(template.FuncMap{
		"q": func(v string) (string, error) {
			// JSON encoding yields a valid YAML double-quoted scalar (handles
			// quotes/colons/#/braces/backslash). Also double any '$' so docker
			// compose's ${VAR} interpolation leaves it literal (GH #322) — else a
			// password like "p@ss$x" would have $x interpolated to "".
			b, e := json.Marshal(v)
			if e != nil {
				return "", e
			}
			return strings.ReplaceAll(string(b), "$", "$$"), nil
		},
		// userinfo percent-encodes a value for the user or password part of a
		// URL (DATABASE_URL, EMAIL_SERVER), so a credential holding '@', ':',
		// '/', '%' or a space decodes back to itself. urlquery is not enough:
		// it turns a space into '+', which userinfo decoding keeps as '+'.
		"userinfo": func(v string) string {
			return strings.ReplaceAll(url.QueryEscape(v), "+", "%20")
		},
		"hasPrefix": strings.HasPrefix,
	}).Parse(entry.ComposeTemplate())
	if err != nil {
		return "", fmt.Errorf("parse compose template for %q: %w", entry.Slug, err)
	}
	// Docker rejects a deploy.resources.limits.cpus value greater than
	// the host's CPU count ("range of CPUs is from 0.01 to N.00, as
	// there are only N CPUs available"), which fails the whole app at
	// `compose up`. A catalog default like ownCloud's "2.0" therefore
	// bricks install on any 1-vCPU box (GH#178). Clamp to the host here
	// — the single chokepoint every render path (install, update,
	// reconcile, CLI) flows through. panel-api runs on the docker host
	// in jabali's single-box model, so runtime.NumCPU() is the daemon's
	// CPU count.
	params.CPULimit = clampCPULimit(params.CPULimit, runtime.NumCPU())
	// A bare number in deploy.resources.limits.memory means BYTES to
	// docker compose — an operator typing "1024" (meaning MB) gets a
	// 1 KiB limit and an instantly-OOM-killed container. Catalog
	// defaults carry a unit ("512m"); normalise unitless operator input
	// to megabytes at the same chokepoint as the CPU clamp.
	params.MemoryLimit = normalizeMemoryLimit(params.MemoryLimit)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return "", fmt.Errorf("execute compose template for %q: %w", entry.Slug, err)
	}
	// Reject invalid YAML at render time — most importantly duplicate
	// mapping keys, which a template emits when it hardcodes an env var
	// that also arrives via .Env (the n8n N8N_HOST incident: docker
	// compose refused the file and the install failed as an opaque
	// "pull exit status 1"). yaml.v3 errors on duplicates by default,
	// so this turns a broken install into a clear render error.
	var probe map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &probe); err != nil {
		return "", fmt.Errorf("rendered compose for %q is not valid YAML: %w", entry.Slug, err)
	}
	return buf.String(), nil
}

// adminDroppedCaps are capabilities Docker grants every container by default
// that no catalog web app needs. NET_RAW lets a container forge packets on the
// bridge (ARP/DNS spoofing of its neighbours); MKNOD lets it create device
// nodes.
var adminDroppedCaps = []string{"NET_RAW", "MKNOD"}

// applyAdminHardening is the baseline every admin-installed app gets. A tenant
// install gets the full applyTenantHardening profile instead. An admin app is
// installed by a trusted admin from the curated catalog, but it still runs
// internet-facing code. Without user-namespace remapping (hosts without tenant
// Docker), root in its container is root on the host. So these are applied
// to every service:
//
//   - no-new-privileges, so a setuid binary in the image cannot raise a
//     process's privileges;
//   - drop NET_RAW and MKNOD;
//   - the install's PIDs limit. It was stored and shown in the panel, but no
//     template used it, so it had no effect. It applies to each service, so
//     the catalog's pids value must fit the app's busiest service: plausible's
//     ClickHouse runs about 670 threads idle and crashes when it cannot start
//     one.
//
// Entries the template already sets are kept: a service's own security_opt
// and cap_drop are extended, not replaced, and a cap_add the template asked
// for explicitly is not dropped. The compose is edited as a YAML node tree,
// not decoded into a map and re-encoded, so the template's key order, quoting
// and comments survive: the file the agent writes stays the template output
// plus these keys.
func applyAdminHardening(composeYAML string, pidsLimit int) (string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &root); err != nil {
		return "", fmt.Errorf("parse rendered compose for hardening: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("rendered compose is not a mapping")
	}
	svcs := mappingValue(root.Content[0], "services")
	if svcs == nil {
		return "", fmt.Errorf("rendered compose has no services block")
	}
	if svcs.Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose services block is not a mapping")
	}
	for i := 0; i+1 < len(svcs.Content); i += 2 {
		name, svc := svcs.Content[i].Value, svcs.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			return "", fmt.Errorf("compose service %q is not a mapping", name)
		}
		if err := ensureInSequence(svc, "security_opt", "no-new-privileges:true"); err != nil {
			return "", fmt.Errorf("service %q: %w", name, err)
		}
		added := map[string]bool{}
		if ca := mappingValue(svc, "cap_add"); ca != nil && ca.Kind == yaml.SequenceNode {
			for _, c := range ca.Content {
				added[strings.ToUpper(strings.TrimPrefix(c.Value, "CAP_"))] = true
			}
		}
		for _, c := range adminDroppedCaps {
			if added[c] {
				continue
			}
			if err := ensureInSequence(svc, "cap_drop", c); err != nil {
				return "", fmt.Errorf("service %q: %w", name, err)
			}
		}
		if pidsLimit > 0 {
			// deploy.resources.limits.pids, never the legacy top-level key:
			// compose v5 rejects both together (GH #284).
			limits, err := ensureMapping(svc, "deploy", "resources", "limits")
			if err != nil {
				return "", fmt.Errorf("service %q: %w", name, err)
			}
			setMappingScalar(limits, "pids", strconv.Itoa(pidsLimit), "!!int")
			deleteMappingKey(svc, "pids_limit")
		}
	}
	literalMultiline(&root)
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return "", fmt.Errorf("re-encode hardened compose: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("re-encode hardened compose: %w", err)
	}
	return out.String(), nil
}

// literalMultiline re-styles every multi-line string as a literal block. A
// template's multi-line command written as a folded double-quoted scalar does
// not survive yaml.v3 re-encoding in its own style: the encoder re-folds it and
// the value changes (an extra newline inside plausible's and erpnext's
// commands). A literal block holds the value exactly.
func literalMultiline(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" && strings.Contains(n.Value, "\n") {
		n.Style = yaml.LiteralStyle
	}
	for _, c := range n.Content {
		literalMultiline(c)
	}
}

// mappingValue returns the value node for key in a mapping node, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// ensureInSequence makes key a sequence in mapping m that holds v, keeping
// whatever it already held.
func ensureInSequence(m *yaml.Node, key, v string) error {
	seq := mappingValue(m, key)
	if seq == nil {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, seq)
	}
	if seq.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s is not a list", key)
	}
	for _, e := range seq.Content {
		if e.Value == v {
			return nil
		}
	}
	seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	return nil
}

// ensureMapping walks (creating as needed) nested mappings under m along path
// and returns the last one.
func ensureMapping(m *yaml.Node, path ...string) (*yaml.Node, error) {
	cur := m
	for _, key := range path {
		next := mappingValue(cur, key)
		if next == nil {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			cur.Content = append(cur.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, next)
		}
		if next.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s is not a mapping", key)
		}
		cur = next
	}
	return cur, nil
}

// setMappingScalar sets key in mapping m to a scalar, replacing any value.
func setMappingScalar(m *yaml.Node, key, value, tag string) {
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

// deleteMappingKey removes key from mapping m if present.
func deleteMappingKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// normalizeMemoryLimit appends "m" to an all-digit memory limit so the
// operator's "1024" means megabytes, not bytes. Values with a unit (or
// empty) pass through untouched.
func normalizeMemoryLimit(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return v
		}
	}
	return v + "m"
}

// applyTenantHardening re-parses the rendered compose and injects the M49
// security profile into EVERY service: no-new-privileges, cap_drop ALL +
// the verified cap allowlist, an optional pids cap, and cgroup_parent so the
// container nests in the tenant's M18 slice. Operates on the rendered YAML
// (not the template) so the shared catalog templates stay admin/tenant-neutral.
// Key order is not preserved (Go map) — compose is order-insensitive.
func applyTenantHardening(composeYAML string, h TenantHardening) (string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return "", fmt.Errorf("parse rendered compose for hardening: %w", err)
	}
	svcsRaw, ok := doc["services"]
	if !ok {
		return "", fmt.Errorf("rendered compose has no services block")
	}
	svcs, ok := svcsRaw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("compose services block is not a mapping")
	}
	capAdd := make([]any, len(h.Caps))
	for i, c := range h.Caps {
		capAdd[i] = c
	}
	for name, sRaw := range svcs {
		s, ok := sRaw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("compose service %q is not a mapping", name)
		}
		s["security_opt"] = []any{"no-new-privileges:true"}
		s["cap_drop"] = []any{"ALL"}
		if len(capAdd) > 0 {
			s["cap_add"] = capAdd
		}
		if h.PIDsLimit > 0 {
			// Set the pids cap under deploy.resources.limits.pids (where the
			// template already puts cpus/memory) rather than the legacy
			// top-level pids_limit key. docker compose v5 maps the legacy key
			// onto deploy.resources.limits.pids and then rejects the project
			// with "can't set distinct values" whenever a deploy: block is
			// present — which every tenant app has — stranding the install as
			// failed (GH #284). Drop any stray legacy key for the same reason.
			setDeployPidsLimit(s, h.PIDsLimit)
			delete(s, "pids_limit")
		}
		s["cgroup_parent"] = h.CgroupParent
		svcs[name] = s
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("re-marshal hardened compose: %w", err)
	}
	return string(out), nil
}

// setDeployPidsLimit writes pids into deploy.resources.limits.pids, creating the
// nested maps as needed. docker compose (v2+) applies this limit on a plain
// `compose up`, the same as the cpus/memory limits the templates already use —
// and unlike the legacy top-level pids_limit it does not collide with the
// deploy: block under compose v5 (GH #284).
func setDeployPidsLimit(s map[string]any, pids int) {
	deploy, _ := s["deploy"].(map[string]any)
	if deploy == nil {
		deploy = map[string]any{}
		s["deploy"] = deploy
	}
	res, _ := deploy["resources"].(map[string]any)
	if res == nil {
		res = map[string]any{}
		deploy["resources"] = res
	}
	lim, _ := res["limits"].(map[string]any)
	if lim == nil {
		lim = map[string]any{}
		res["limits"] = lim
	}
	lim["pids"] = pids
}

// clampCPULimit caps the requested cpus value to the host's logical
// CPU count. Docker hard-rejects a limit above the available CPUs, so
// without this a catalog default that exceeds a small VPS's core count
// fails the deploy (GH#178). Empty, unparseable, or already-in-range
// values pass through unchanged; only an over-budget value is lowered,
// to the whole-host count in the two-decimal form Docker accepts.
func clampCPULimit(cpu string, hostCPUs int) string {
	if hostCPUs <= 0 {
		return cpu
	}
	trimmed := strings.TrimSpace(cpu)
	if trimmed == "" {
		return cpu
	}
	v, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || v <= float64(hostCPUs) {
		return cpu
	}
	return strconv.FormatFloat(float64(hostCPUs), 'f', 2, 64)
}

// MaterialiseEnv resolves the catalog's env declarations into a
// concrete name->value map. Catalog defaults are used as-is;
// generate:'password32'/'password64' entries get a fresh random
// secret per install.
func MaterialiseEnv(entry Entry, overrides map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(entry.Env))
	for _, e := range entry.Env {
		if v, ok := overrides[e.Name]; ok {
			out[e.Name] = v
			continue
		}
		if e.Value != "" {
			out[e.Name] = e.Value
			continue
		}
		if e.Generate != "" {
			secret, err := generateSecret(e.Generate)
			if err != nil {
				return nil, fmt.Errorf("generate %s: %w", e.Name, err)
			}
			out[e.Name] = secret
			continue
		}
		// Declared but no value, no generator, no override — leave
		// empty; the compose template can decide whether to omit.
		out[e.Name] = ""
	}
	return out, nil
}

// secretRandRead is crypto/rand.Read; a var so tests can choose the bytes.
var secretRandRead = rand.Read

// secretSchemes are the generate: values generateSecret knows; the catalog
// loader refuses any other.
var secretSchemes = map[string]bool{
	"password32":  true,
	"password64":  true,
	"laravel_key": true,
	"base64_32":   true,
}

func generateSecret(scheme string) (string, error) {
	var nbytes int
	switch scheme {
	case "password32":
		nbytes = 24 // base64-rounded to ~32 chars
	case "password64":
		nbytes = 48 // base64-rounded to ~64 chars
	case "laravel_key":
		// Laravel APP_KEY: literal "base64:" + standard-base64 of exactly 32
		// random bytes (Snipe-IT and other Laravel apps validate this shape).
		buf := make([]byte, 32)
		if _, err := secretRandRead(buf); err != nil {
			return "", err
		}
		return "base64:" + base64.StdEncoding.EncodeToString(buf), nil
	case "base64_32":
		// Standard padded base64 of exactly 32 random bytes, the shape
		// `openssl rand -base64 32` prints (Plausible's TOTP_VAULT_KEY).
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(buf), nil
	default:
		return "", fmt.Errorf("unknown secret scheme %q", scheme)
	}
	// A secret starting with "-" reads as an option when a container passes
	// it on a command line (owncloud: occ maintenance:install --admin-pass
	// "$PW" fails with `The "-j" option does not exist`). base64url starts
	// one secret in 64 that way, so draw again.
	buf := make([]byte, nbytes)
	for {
		if _, err := secretRandRead(buf); err != nil {
			return "", err
		}
		if s := base64.RawURLEncoding.EncodeToString(buf); !strings.HasPrefix(s, "-") {
			return s, nil
		}
	}
}
