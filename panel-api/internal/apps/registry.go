// Package apps holds the M19 application catalog: a single registry of
// "app descriptors" the API uses to decide what to show in the install
// picker, which agent commands to dispatch, and how to validate the
// install form. Each descriptor is registered once at startup; handlers
// then look it up by the `app_type` value persisted on
// models.ApplicationInstall.
//
// The registry is the seam between API and agent for the M19
// generalisation. Adding a new app means: register a descriptor here,
// implement its install/delete/clone agent commands, and the generic
// /applications endpoints + UI picker pick it up automatically.
package apps

import (
	"fmt"
	"sort"
	"sync"
)

// ParamSpec describes one per-app form field beyond the generic
// (domain, subdirectory, admin_email) ones the framework already asks
// for. The UI in step 5 reads Type to pick the AntD control; the API
// in step 3 validates against Required, Pattern, and Values.
type ParamSpec struct {
	// Type is one of "string", "email", "password", "enum", "bool". The
	// validator and the UI both switch on this string — adding a new
	// type means updating both.
	Type string `json:"type"`
	// Required rejects empty/missing values at the API boundary.
	Required bool `json:"required"`
	// Pattern is an optional anchored regex applied to "string" values
	// (and the local-part of "email" values). Nil = no extra check.
	Pattern *string `json:"pattern,omitempty"`
	// Values is the closed set of acceptable values for Type=="enum".
	// Ignored for other types.
	Values []string `json:"values,omitempty"`
	// ValueLabels optionally maps an enum value to its display label
	// (e.g. "IL" → "Israel (IL)"). The UI's searchable Select filters on
	// the label, so large enums become searchable by name instead of by
	// bare code. Values without an entry render as themselves.
	ValueLabels map[string]string `json:"value_labels,omitempty"`
	// Default is sent to the UI as the prefilled control value. Use a
	// JSON-friendly type (string/bool/number); the UI does no decoding.
	Default any `json:"default,omitempty"`
	// Description appears under the field as the AntD Form.Item extra.
	Description string `json:"description,omitempty"`
}

// App is the descriptor registered for one installable application.
// All fields are read-only after Register; the registry copies values
// in to keep callers from mutating shared state.
type App struct {
	// Name is the machine identifier persisted as
	// application_installs.app_type. Must be lowercase, snake_case,
	// stable across releases (renaming = a migration).
	Name string `json:"name"`
	// DisplayName is shown in the install picker and listings.
	DisplayName string `json:"display_name"`
	// Icon is an antd icon name (e.g. "WordPressOutlined") the UI maps
	// to the @ant-design/icons component. Empty = picker fallback.
	Icon string `json:"icon,omitempty"`
	// Description is a one-line tagline shown beneath the icon in the
	// install picker.
	Description string `json:"description,omitempty"`
	// Tags are catalog category/use-case labels for filtering the install
	// picker (GH #448), e.g. ["CMS","Blog"]. Free-form but should reuse the
	// shared vocabulary so the UI filter stays coherent across apps.
	Tags []string `json:"tags,omitempty"`
	// DefaultSubdirectory is the subdirectory the install form
	// pre-fills for this app (e.g. "blog" for WordPress, "" for
	// docroot-default apps). The user can still override.
	DefaultSubdirectory string `json:"default_subdirectory"`
	// RequiresDB controls whether the framework provisions a MariaDB
	// schema + grant before dispatching the install command. Flat-file
	// apps (DokuWiki) set this false.
	RequiresDB bool `json:"requires_db"`
	// EmailLogin marks apps whose admin signs in with their EMAIL, not a
	// username (ITFlow #226). The framework still generates no username
	// for these — it surfaces the admin email as the login credential so
	// the post-install screen doesn't show a meaningless random username.
	EmailLogin bool `json:"email_login,omitempty"`
	// RootOnly marks apps that only work when served from the domain (or
	// subdomain) root: no subdirectory and no www-prefix vhost. ITFlow
	// breaks in a subdir and with www (GH #226). The UI hides the
	// subdirectory + "create www" controls for these apps and the API
	// rejects a non-empty subdirectory / use_www.
	RootOnly bool `json:"root_only,omitempty"`
	// InstallNotice (GH #341) is an optional pre-install warning shown in the
	// install form — e.g. an app that needs PHP exec functions enabled.
	InstallNotice string `json:"install_notice,omitempty"`
	// SupportedPHPVersions, if non-empty, is the closed set of PHP
	// versions this app's installer can target. Empty = "any active
	// pool". The UI uses this to filter the PHP-version picker.
	SupportedPHPVersions []string `json:"supported_php_versions,omitempty"`
	// Agent command names the dispatcher in step 4 routes to. The
	// agent registers handlers for the same command strings.
	AgentInstallCmd string `json:"-"`
	AgentDeleteCmd  string `json:"-"`
	AgentCloneCmd   string `json:"-"`
	// InstallParamSchema is the per-app extension of the install form.
	// Keys are JSON field names sent by the UI; the API validates
	// against this schema before dispatching to the agent.
	InstallParamSchema map[string]ParamSpec `json:"install_param_schema,omitempty"`
}

// Registry is the in-memory catalog. Safe for concurrent reads after
// startup; Register takes a write lock so test wiring can register
// from multiple goroutines without races.
type Registry struct {
	mu   sync.RWMutex
	apps map[string]App
}

// New returns an empty registry. Callers register descriptors during
// startup wiring (typically in app.NewWithDeps).
func New() *Registry {
	return &Registry{apps: make(map[string]App)}
}

// Register inserts a descriptor. Returns an error on duplicate Name or
// invalid descriptor (empty Name, bad ParamSpec.Type, enum without
// Values). The registry copies the descriptor; callers may not mutate
// it after Register returns.
func (r *Registry) Register(d App) error {
	if d.Name == "" {
		return fmt.Errorf("apps.Register: descriptor.Name is required")
	}
	for fieldName, spec := range d.InstallParamSchema {
		if err := validateParamSpec(fieldName, spec); err != nil {
			return fmt.Errorf("apps.Register %q: %w", d.Name, err)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.apps[d.Name]; ok {
		return fmt.Errorf("apps.Register: duplicate app name %q", d.Name)
	}
	r.apps[d.Name] = d
	return nil
}

// Get returns the descriptor for name and whether it was registered.
// The returned App is a copy; mutating it has no effect on the
// registry.
func (r *Registry) Get(name string) (App, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.apps[name]
	return d, ok
}

// List returns all registered descriptors sorted by Name. The UI
// install picker calls GET /applications/registry which iterates
// over this; alphabetical order keeps the picker stable across
// startups.
func (r *Registry) List() []App {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]App, 0, len(r.apps))
	for _, d := range r.apps {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// validateParamSpec rejects descriptors the UI/API can't render or
// validate. Kept here (not on ParamSpec) because the failure mode is a
// startup panic via Register's error return — a developer mistake, not
// a runtime branch.
func validateParamSpec(field string, s ParamSpec) error {
	switch s.Type {
	case "string", "email", "password", "bool":
		if len(s.Values) > 0 {
			return fmt.Errorf("param %q: Values only valid for enum", field)
		}
	case "enum":
		if len(s.Values) == 0 {
			return fmt.Errorf("param %q: enum requires Values", field)
		}
	default:
		return fmt.Errorf("param %q: unknown Type %q", field, s.Type)
	}
	return nil
}
