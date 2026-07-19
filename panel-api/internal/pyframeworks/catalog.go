// Package pyframeworks loads + validates the Python WSGI/ASGI framework
// marketplace catalog (JAB-164): install/py-frameworks/<slug>/framework.yaml.
//
// It's the Step-1 foundation for a one-click framework marketplace on top of the
// existing per-user Python-app runtime (ADR-0131). The catalog only adds a
// catalog + scaffold description; provisioning reuses the shipped venv +
// loopback-port + nginx-proxy flow. Mirrors internal/dockerapp/catalog.go: the
// catalog is read once at startup, immutable after load, and bad entries are
// logged + skipped rather than crashing boot.
package pyframeworks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// App server types the runtime already knows how to launch.
const (
	AppTypeWSGI = "wsgi"
	AppTypeASGI = "asgi"
)

// EnvVar is one templated environment variable an entry declares. Secret vars
// (e.g. Django SECRET_KEY) are GENERATED at install time, never stored in the
// catalog — `generate` marks them so the scaffolder mints a value into the
// app's env instead of using Default.
type EnvVar struct {
	Key         string `yaml:"key"`
	Default     string `yaml:"default,omitempty"`
	Description string `yaml:"description,omitempty"`
	Generate    string `yaml:"generate,omitempty"` // "" | "secret_key" | "hex32"
	Required    bool   `yaml:"required,omitempty"`
}

// Entry is the parsed shape of <slug>/framework.yaml.
type Entry struct {
	Slug        string   `yaml:"slug"`
	Name        string   `yaml:"name"`
	Version     string   `yaml:"version"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags,omitempty"`
	// Popularity orders the catalog (higher first, ties by slug). Optional.
	Popularity int    `yaml:"popularity,omitempty"`
	Icon       string `yaml:"icon,omitempty"`
	Upstream   string `yaml:"upstream,omitempty"`
	Docs       string `yaml:"docs,omitempty"`

	// AppType wires to the runtime's launch model: wsgi → gunicorn, asgi →
	// uvicorn/hypercorn/daphne.
	AppType string `yaml:"app_type"`
	// Server is the ASGI/WSGI server binary installed into the venv.
	Server string `yaml:"server"`
	// PythonMin is the minimum Python runtime (e.g. "3.10"); the picker gates on
	// host-available runtimes (GH#357 scar: never offer an uninstalled runtime).
	PythonMin string `yaml:"python_min,omitempty"`
	// PipRequirements are the pinned top-level deps written to requirements.txt.
	PipRequirements []string `yaml:"pip_requirements"`

	// Scaffold describes how a starter project is created into app_root. Kind
	// "cmd" runs Command (e.g. django-admin startproject); "template" copies the
	// entry's template/ dir. Both run AS THE TENANT.
	Scaffold Scaffold `yaml:"scaffold"`
	// EntrypointTemplate is the module:callable the server launches, with
	// {{.Project}} substituted for the scaffolded project name (e.g.
	// "{{.Project}}.wsgi:application", "app:app", "main:app").
	EntrypointTemplate string `yaml:"entrypoint_template"`

	DefaultEnv []EnvVar `yaml:"default_env,omitempty"`

	// NeedsDB: none|sqlite|mariadb|postgres. mariadb/postgres optionally link a
	// Jabali DB instead of SQLite (Step 4).
	NeedsDB string `yaml:"needs_db,omitempty"`
	// StaticURL/StaticRoot: for Django-family entries, nginx serves StaticURL
	// from StaticRoot (a location alongside the proxy). Empty = no static split.
	StaticURL  string `yaml:"static_url,omitempty"`
	StaticRoot string `yaml:"static_root,omitempty"`
	// PostInstall are ordered steps run after pip install (e.g. migrate,
	// collectstatic). Free-form tokens the scaffolder maps to framework actions.
	PostInstall []string `yaml:"post_install,omitempty"`

	// TemplateFiles is relpath->content for a kind=template scaffold, loaded from
	// <slug>/template/** by LoadDir (NOT the YAML). Kept in-memory on the entry so
	// the reconciler can ship the starter over agentwire without touching disk,
	// mirroring the docker-app catalog's inline templates.
	TemplateFiles map[string]string `yaml:"-"`
}

// Scaffold is how a starter project is generated.
type Scaffold struct {
	Kind    string `yaml:"kind"`              // "cmd" | "template"
	Command string `yaml:"command,omitempty"` // for kind=cmd, {{.Project}} templated
	Project string `yaml:"project,omitempty"` // default project name for cmd scaffolds
}

var (
	slugRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	pyVerRe = regexp.MustCompile(`^3\.[0-9]{1,2}$`)
	// wsgiServers/asgiServers gate server-vs-type consistency.
	wsgiServers = map[string]bool{"gunicorn": true}
	asgiServers = map[string]bool{"uvicorn": true, "hypercorn": true, "daphne": true}
	needsDBSet  = map[string]bool{"": true, "none": true, "sqlite": true, "mariadb": true, "postgres": true}
)

func (e Entry) validate() error {
	if !slugRe.MatchString(e.Slug) {
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
	switch e.AppType {
	case AppTypeWSGI, AppTypeASGI:
	default:
		return fmt.Errorf("app_type %q: must be wsgi or asgi", e.AppType)
	}
	// Server must match the app type — a WSGI server can't run an ASGI app.
	if e.AppType == AppTypeWSGI && !wsgiServers[e.Server] {
		return fmt.Errorf("server %q: WSGI entries require gunicorn", e.Server)
	}
	if e.AppType == AppTypeASGI && !asgiServers[e.Server] {
		return fmt.Errorf("server %q: ASGI entries require uvicorn, hypercorn, or daphne", e.Server)
	}
	if e.PythonMin != "" && !pyVerRe.MatchString(e.PythonMin) {
		return fmt.Errorf("python_min %q: must be like 3.10", e.PythonMin)
	}
	if len(e.PipRequirements) == 0 {
		return errors.New("pip_requirements must list at least the framework + server")
	}
	switch e.Scaffold.Kind {
	case "cmd":
		if e.Scaffold.Command == "" {
			return errors.New("scaffold.command is required for kind=cmd")
		}
	case "template":
		if len(e.TemplateFiles) == 0 {
			return errors.New("scaffold.kind=template requires a non-empty template/ dir")
		}
	default:
		return fmt.Errorf("scaffold.kind %q: must be cmd or template", e.Scaffold.Kind)
	}
	if e.EntrypointTemplate == "" {
		return errors.New("entrypoint_template is required")
	}
	if !needsDBSet[e.NeedsDB] {
		return fmt.Errorf("needs_db %q: must be none, sqlite, mariadb, or postgres", e.NeedsDB)
	}
	// Static split is all-or-nothing.
	if (e.StaticURL == "") != (e.StaticRoot == "") {
		return errors.New("static_url and static_root must be set together")
	}
	// Generated env vars must name a supported generator.
	for _, v := range e.DefaultEnv {
		if v.Key == "" {
			return errors.New("default_env: key is required")
		}
		switch v.Generate {
		case "", "secret_key", "hex32":
		default:
			return fmt.Errorf("default_env[%s].generate %q: unsupported", v.Key, v.Generate)
		}
	}
	return nil
}

// readTemplateDir reads every regular file under dir into a relpath->content
// map (forward-slash keys). The agent re-validates each relpath before writing
// (no traversal, not absolute), so this only needs to reject the unreadable.
func readTemplateDir(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("template/%s is not a regular file", d.Name())
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read template dir: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("template/ dir is empty")
	}
	return out, nil
}

// Catalog is the loaded, immutable set of framework entries.
type Catalog struct {
	bySlug map[string]Entry
	sorted []Entry
}

// Get returns an entry by slug.
func (c *Catalog) Get(slug string) (Entry, bool) { e, ok := c.bySlug[slug]; return e, ok }

// All returns the entries in listing order (popularity desc, then slug).
func (c *Catalog) All() []Entry { return c.sorted }

// Len is the number of loaded entries.
func (c *Catalog) Len() int { return len(c.sorted) }

// EntryError pairs a slug (or path) with why it failed to load, so boot can log
// + skip without crashing.
type EntryError struct {
	Slug string
	Err  error
}

func (e EntryError) Error() string { return fmt.Sprintf("%s: %v", e.Slug, e.Err) }

// LoadDir reads every <root>/<slug>/framework.yaml, validates it, and returns
// the catalog plus a slice of per-entry errors for the ones that were skipped.
func LoadDir(root string) (*Catalog, []EntryError) {
	c := &Catalog{bySlug: map[string]Entry{}}
	var errs []EntryError

	dirs, err := os.ReadDir(root)
	if err != nil {
		return c, []EntryError{{Slug: root, Err: err}}
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		slug := d.Name()
		path := filepath.Join(root, slug, "framework.yaml")
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			errs = append(errs, EntryError{Slug: slug, Err: rerr})
			continue
		}
		var e Entry
		if uerr := yaml.Unmarshal(raw, &e); uerr != nil {
			errs = append(errs, EntryError{Slug: slug, Err: uerr})
			continue
		}
		if e.Slug != slug {
			errs = append(errs, EntryError{Slug: slug, Err: fmt.Errorf("slug %q does not match dir %q", e.Slug, slug)})
			continue
		}
		// Load the starter files for a template scaffold (validated below).
		if e.Scaffold.Kind == "template" {
			tf, terr := readTemplateDir(filepath.Join(root, slug, "template"))
			if terr != nil {
				errs = append(errs, EntryError{Slug: slug, Err: terr})
				continue
			}
			e.TemplateFiles = tf
		}
		if verr := e.validate(); verr != nil {
			errs = append(errs, EntryError{Slug: slug, Err: verr})
			continue
		}
		if _, dup := c.bySlug[slug]; dup {
			errs = append(errs, EntryError{Slug: slug, Err: errors.New("duplicate slug")})
			continue
		}
		c.bySlug[slug] = e
		c.sorted = append(c.sorted, e)
	}
	sort.SliceStable(c.sorted, func(i, j int) bool {
		if c.sorted[i].Popularity != c.sorted[j].Popularity {
			return c.sorted[i].Popularity > c.sorted[j].Popularity
		}
		return c.sorted[i].Slug < c.sorted[j].Slug
	})
	return c, errs
}
