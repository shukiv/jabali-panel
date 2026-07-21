// Package dockerapp loads + validates the M48 docker-app catalog
// (install/docker-apps/<slug>/{app.yaml,compose.yml.tmpl,icon.svg}).
//
// The catalog is read at panel-api startup; entries that fail
// validation are logged and skipped — they don't crash the boot.
// Catalog data is treated as immutable once loaded; admin REST
// handlers and the reconciler ask the loaded Catalog for entries
// instead of re-reading files on every request.
//
// See ADR-0116 for decision rationale (single-source catalog,
// in-repo, no external registry in v1).
package dockerapp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entry is the parsed shape of <slug>/app.yaml.
type Entry struct {
	Slug        string   `yaml:"slug"`
	Name        string   `yaml:"name"`
	Version     string   `yaml:"version"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	// Popularity orders the catalog: higher floats to the top of the listing,
	// ties break alphabetically by slug. Optional (default 0). Operator-tunable
	// per app.yaml so the most-installed apps surface first.
	Popularity    int    `yaml:"popularity,omitempty" json:"popularity,omitempty"`
	Icon          string `yaml:"icon,omitempty"`
	Upstream      string `yaml:"upstream,omitempty"`
	Documentation string `yaml:"documentation,omitempty"`
	// PostInstallNote is optional first-run guidance shown in the panel AFTER a
	// successful install (GH #521, e.g. Pocket ID: create the first admin at
	// https://<domain>/setup). Plain text; a literal "{{domain}}" token is
	// substituted with the app's assigned domain when the note is served.
	PostInstallNote string     `yaml:"post_install_note,omitempty" json:"post_install_note,omitempty"`
	ImageChannel    string     `yaml:"image_channel"`
	UpdateMode      string     `yaml:"update_mode,omitempty"`
	Resources       Resources  `yaml:"resources,omitempty"`
	Volumes         []Volume   `yaml:"volumes"`
	Ports           []PortSpec `yaml:"ports"`
	Env             []EnvVar   `yaml:"env,omitempty"`
	// SMTP (GH #322), when true, auto-injects the standard SMTP_* env vars
	// (host/port/user/password/from/from_name/tls) as optional, operator-
	// supplied install fields. The app's compose.yml.tmpl maps them to its own
	// SMTP env, guarded by {{ if (index .Env "SMTP_HOST") }}.
	SMTP bool `yaml:"smtp,omitempty" json:"smtp,omitempty"`
	// TenantInstallable (M49, GH #170): when true, this app appears in the
	// TENANT catalog and a tenant may install it. Default false — admin-only,
	// the M48 behaviour. Only flip true for an app verified to run under the
	// tenant hardening profile (cap_drop ALL + TenantCaps, no-new-privileges,
	// loopback-only, no host mounts). Apps with a default_bind:public port can
	// never be tenant_installable (loopback-only requirement, enforced by the
	// tenant catalog filter).
	TenantInstallable bool `yaml:"tenant_installable,omitempty"`
	// TenantCaps is the verified minimal Linux capability allowlist added back
	// after cap_drop:ALL for tenant installs (e.g. ["CHOWN","SETUID","SETGID",
	// "DAC_OVERRIDE"] for images that chown-then-drop). Established by actually
	// running the app under drop-ALL, NOT guessed. Empty = needs no caps.
	TenantCaps []string `yaml:"tenant_caps,omitempty"`
	// VolumeOwner is an optional "uid:gid" pair the agent applies
	// to /var/lib/jabali/docker-apps/<slug>/<volume>/ before docker
	// compose up runs. Empty = leave the dirs root:root. Set for
	// images whose entrypoint runs as a non-root UID and reads from
	// the bind-mounted volume on first launch (Gitea, Nextcloud,
	// Linkwarden, ...) -- without this they crash-loop on EACCES.
	VolumeOwner string `yaml:"volume_owner,omitempty"`

	// composeTmpl is the raw text of compose.yml.tmpl alongside the
	// app.yaml. Held in memory so the agent verb that renders the
	// compose doesn't have to walk the filesystem at apply time.
	composeTmpl string
	// dir is the absolute path to this entry's directory; used to
	// resolve relative paths (icon, etc.) at handler time.
	dir string
}

type Resources struct {
	CPU    string `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"`
	PIDs   int    `yaml:"pids,omitempty" json:"pids,omitempty"`
}

type Volume struct {
	Name          string `yaml:"name" json:"name"`
	ContainerPath string `yaml:"container_path" json:"container_path"`
}

type PortSpec struct {
	Name                string `yaml:"name" json:"name"`
	ContainerPort       int    `yaml:"container_port" json:"container_port"`
	Protocol            string `yaml:"protocol" json:"protocol"`
	DefaultEnabled      bool   `yaml:"default_enabled" json:"default_enabled"`
	DefaultBind         string `yaml:"default_bind" json:"default_bind"`
	DefaultReverseProxy bool   `yaml:"default_reverse_proxy" json:"default_reverse_proxy"`
	HealthPath          string `yaml:"health_path,omitempty" json:"health_path,omitempty"`
}

type EnvVar struct {
	Name     string `yaml:"name" json:"name"`
	Value    string `yaml:"value,omitempty" json:"value,omitempty"`
	Secret   bool   `yaml:"secret,omitempty" json:"secret,omitempty"`
	Generate string `yaml:"generate,omitempty" json:"generate,omitempty"`
}

// ComposeTemplate returns the raw compose.yml.tmpl content. The agent
// renders it via Go text/template.
func (e Entry) ComposeTemplate() string { return e.composeTmpl }

// Dir returns the absolute path of this entry's directory.
func (e Entry) Dir() string { return e.dir }

// Catalog is the loaded, validated set of catalog entries. Lookups
// are O(1). Iteration order is deterministic (sorted by slug).
type Catalog struct {
	bySlug map[string]Entry
	order  []string
}

// Get returns the entry for a slug; (Entry, true) on hit, (zero, false)
// on miss. Callers should treat misses as "unknown catalog entry —
// 404 the request" rather than "internal error" — entries can be
// removed from the panel between two panel deploys.
func (c *Catalog) Get(slug string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	e, ok := c.bySlug[slug]
	return e, ok
}

// All returns every entry, slug-sorted for deterministic UI listing.
func (c *Catalog) All() []Entry {
	if c == nil {
		return nil
	}
	out := make([]Entry, 0, len(c.order))
	for _, s := range c.order {
		out = append(out, c.bySlug[s])
	}
	return out
}

// Len returns the number of loaded entries.
func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.bySlug)
}

// LoadDir walks `root` (typically install/docker-apps/ in repo dev,
// or /usr/local/share/jabali/docker-apps/ in production) and parses
// every <slug>/app.yaml that validates. Returns the populated
// Catalog plus a slice of per-directory load errors (don't fail the
// whole catalog because one entry is malformed — log + skip).
func LoadDir(root string) (*Catalog, []EntryError) {
	c := &Catalog{bySlug: make(map[string]Entry)}
	var errs []EntryError

	entries, err := os.ReadDir(root)
	if err != nil {
		return c, []EntryError{{Slug: "", Err: fmt.Errorf("read catalog root %q: %w", root, err)}}
	}

	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		// Skip directories starting with _ (e.g. _schema/).
		if strings.HasPrefix(ent.Name(), "_") {
			continue
		}
		dir := filepath.Join(root, ent.Name())
		e, lerr := loadOne(dir)
		if lerr != nil {
			errs = append(errs, EntryError{Slug: ent.Name(), Err: lerr})
			continue
		}
		// Cross-check slug vs directory name. A mismatch is almost
		// always a copy-paste mistake.
		if e.Slug != ent.Name() {
			errs = append(errs, EntryError{Slug: ent.Name(), Err: fmt.Errorf("slug %q does not match directory name %q", e.Slug, ent.Name())})
			continue
		}
		if _, dup := c.bySlug[e.Slug]; dup {
			errs = append(errs, EntryError{Slug: e.Slug, Err: errors.New("duplicate slug")})
			continue
		}
		c.bySlug[e.Slug] = e
		c.order = append(c.order, e.Slug)
	}

	// Order the catalog by popularity (desc) so the most-installed apps surface
	// first, ties alphabetical by slug for a stable listing.
	sort.SliceStable(c.order, func(i, j int) bool {
		a, b := c.bySlug[c.order[i]], c.bySlug[c.order[j]]
		if a.Popularity != b.Popularity {
			return a.Popularity > b.Popularity
		}
		return a.Slug < b.Slug
	})
	return c, errs
}

// EntryError carries a load failure for one directory.
type EntryError struct {
	Slug string
	Err  error
}

func (e EntryError) Error() string {
	if e.Slug == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("docker-app %q: %v", e.Slug, e.Err)
}

func loadOne(dir string) (Entry, error) {
	yamlPath := filepath.Join(dir, "app.yaml")
	yamlBytes, err := os.ReadFile(yamlPath)
	if err != nil {
		return Entry{}, fmt.Errorf("read app.yaml: %w", err)
	}
	var e Entry
	if err := yaml.Unmarshal(yamlBytes, &e); err != nil {
		return Entry{}, fmt.Errorf("parse app.yaml: %w", err)
	}
	// GH #322: smtp:true auto-injects the standard operator-supplied SMTP_* env.
	if e.SMTP {
		e.Env = injectStandardSMTPEnv(e.Env)
	}
	if err := e.validate(); err != nil {
		return Entry{}, err
	}

	tmplPath := filepath.Join(dir, "compose.yml.tmpl")
	tmplBytes, err := os.ReadFile(tmplPath)
	if err != nil {
		return Entry{}, fmt.Errorf("read compose.yml.tmpl: %w", err)
	}
	e.composeTmpl = string(tmplBytes)
	e.dir = dir

	// Icon is required by the schema; verify the file exists. Defaults
	// to icon.svg if not specified in app.yaml.
	iconName := e.Icon
	if iconName == "" {
		iconName = "icon.svg"
		e.Icon = iconName
	}
	if _, ierr := os.Stat(filepath.Join(dir, iconName)); ierr != nil {
		if errors.Is(ierr, fs.ErrNotExist) {
			return Entry{}, fmt.Errorf("icon %q not found in %s", iconName, dir)
		}
		return Entry{}, fmt.Errorf("stat icon %q: %w", iconName, ierr)
	}

	// Gitea #530: every literal service image in the compose must be digest-pinned
	// too, not just image_channel. A templated image ({{ .ImageChannel }}) resolves
	// to the already-validated image_channel; any literal sidecar/support image
	// (e.g. postgres) must carry an @sha256 digest so a future app can't ship a
	// mutable tag and pass loading.
	if err := validateComposeImages(e.composeTmpl); err != nil {
		return Entry{}, err
	}

	if e.UpdateMode == "" {
		e.UpdateMode = "manual"
	}
	return e, nil
}

// composeImageRe captures the value of an `image:` key in a compose template.
var composeImageRe = regexp.MustCompile(`(?m)^\s*image:\s*(\S+)`)

// validateComposeImages requires every LITERAL service image in the compose
// template to be digest-pinned (Gitea #530). Templated images (containing
// "{{") resolve to image_channel, which validatePinnedImage already covers.
func validateComposeImages(tmpl string) error {
	for _, m := range composeImageRe.FindAllStringSubmatch(tmpl, -1) {
		img := strings.Trim(strings.TrimSpace(m[1]), "\"'")
		if strings.Contains(img, "{{") {
			continue // templated -> image_channel, validated separately
		}
		if !imageDigestRe.MatchString(img) {
			return fmt.Errorf("compose image %q must be digest-pinned: repo:tag@sha256:<digest>", img)
		}
	}
	return nil
}

// validate enforces the rules from the JSON schema. We re-check here
// because the loader doesn't actually run the JSON-schema validator
// at runtime — a dedicated test (TestCatalogLoad_ValidatesAllEntries)
// validates every catalog entry against the schema in CI, which is
// cheaper than dragging in a JSON-schema dependency at hot-path.

// imageDigestRe requires an image reference to be digest-pinned
// (repo[:tag]@sha256:<64 hex>) so a catalog install always resolves to the
// exact reviewed image bytes — never a mutable tag that could be repushed or
// regress (Gitea #458). A digest-pinned ref also makes ":latest" / floating
// tags impossible to ship.
var imageDigestRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// validatePinnedImage enforces digest pinning on a catalog image reference.
func validatePinnedImage(ref string) error {
	if !imageDigestRe.MatchString(ref) {
		return errors.New("must be digest-pinned: repo:tag@sha256:<digest>")
	}
	return nil
}

func (e Entry) validate() error {
	if !isValidSlug(e.Slug) {
		return fmt.Errorf("slug %q: must match ^[a-z][a-z0-9-]{1,31}$", e.Slug)
	}
	if e.Name == "" {
		return errors.New("name is required")
	}
	if e.Version == "" {
		return errors.New("version is required")
	}
	if e.Description == "" {
		return errors.New("description is required")
	}
	if e.ImageChannel == "" {
		return errors.New("image_channel is required")
	}
	if err := validatePinnedImage(e.ImageChannel); err != nil {
		return fmt.Errorf("image_channel %q: %w", e.ImageChannel, err)
	}
	if e.UpdateMode != "" && e.UpdateMode != "manual" && e.UpdateMode != "auto" {
		return fmt.Errorf("update_mode %q: must be 'manual' or 'auto'", e.UpdateMode)
	}
	// Tenant-installable apps may not declare a forbidden capability (Gitea
	// #515): the catalog is the policy boundary, so reject the dangerous cap at
	// load rather than relying on the runtime allowlist drop alone.
	if e.TenantInstallable {
		for _, c := range e.TenantCaps {
			if IsForbiddenTenantCap(c) {
				return fmt.Errorf("tenant_caps %q is forbidden for tenant_installable apps", c)
			}
		}
	}
	if len(e.Volumes) == 0 {
		return errors.New("at least one volume is required")
	}
	for i, v := range e.Volumes {
		if !isValidVolumeName(v.Name) {
			return fmt.Errorf("volumes[%d].name %q: must match ^[a-z][a-z0-9-]{0,31}$", i, v.Name)
		}
		if !strings.HasPrefix(v.ContainerPath, "/") {
			return fmt.Errorf("volumes[%d].container_path %q: must be absolute", i, v.ContainerPath)
		}
	}
	if len(e.Ports) == 0 {
		return errors.New("at least one port is required")
	}
	seenPorts := make(map[string]struct{}, len(e.Ports))
	for i, p := range e.Ports {
		if !isValidPortName(p.Name) {
			return fmt.Errorf("ports[%d].name %q: must match ^[a-z][a-z0-9-]{0,31}$", i, p.Name)
		}
		if _, dup := seenPorts[p.Name]; dup {
			return fmt.Errorf("ports[%d].name %q: duplicate port name", i, p.Name)
		}
		seenPorts[p.Name] = struct{}{}
		if p.ContainerPort < 1 || p.ContainerPort > 65535 {
			return fmt.Errorf("ports[%d].container_port %d: out of range", i, p.ContainerPort)
		}
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			return fmt.Errorf("ports[%d].protocol %q: must be 'tcp' or 'udp'", i, p.Protocol)
		}
		if p.DefaultBind != "loopback" && p.DefaultBind != "public" {
			return fmt.Errorf("ports[%d].default_bind %q: must be 'loopback' or 'public'", i, p.DefaultBind)
		}
		if p.DefaultReverseProxy && p.DefaultBind == "public" {
			return fmt.Errorf("ports[%d]: default_reverse_proxy=true is incompatible with default_bind='public' (nginx can't proxy a public bind)", i)
		}
		if p.DefaultReverseProxy && p.Protocol == "udp" {
			return fmt.Errorf("ports[%d]: default_reverse_proxy=true is incompatible with protocol=udp (nginx doesn't terminate UDP)", i)
		}
	}
	return nil
}

func isValidSlug(s string) bool {
	if len(s) < 2 || len(s) > 32 {
		return false
	}
	if !(s[0] >= 'a' && s[0] <= 'z') {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

func isValidVolumeName(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	if !(s[0] >= 'a' && s[0] <= 'z') {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

func isValidPortName(s string) bool { return isValidVolumeName(s) }

// standardSMTPEnvNames lists the panel-standard, operator-supplied SMTP env
// keys auto-injected for apps that declare `smtp: true` (GH #322). Each app's
// compose.yml.tmpl maps these to its own SMTP env, guarded by an
// `{{ if (index .Env "SMTP_HOST") }}` block so they're a no-op when unset.
var standardSMTPEnvNames = []string{
	"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD",
	"SMTP_FROM", "SMTP_FROM_NAME", "SMTP_TLS",
}

// injectStandardSMTPEnv appends the standard SMTP_* env vars (operator-supplied,
// no default/generator) to the entry's env, skipping any an app already
// declares explicitly (so an app can pin a specific one). SMTP_PASSWORD is
// marked secret so the UI masks it and the env view redacts it.
func injectStandardSMTPEnv(existing []EnvVar) []EnvVar {
	have := make(map[string]bool, len(existing))
	for _, e := range existing {
		have[e.Name] = true
	}
	out := existing
	for _, name := range standardSMTPEnvNames {
		if have[name] {
			continue
		}
		out = append(out, EnvVar{Name: name, Secret: name == "SMTP_PASSWORD"})
	}
	return out
}
