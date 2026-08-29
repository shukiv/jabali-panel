package pyframeworks

import (
	"fmt"
	"regexp"
	"strings"
)

// CreateSpec is the resolved runtime config for provisioning a Python app from a
// framework entry: the fields the create flow stamps on the app row + the plan
// the agent executes (scaffold, requirements, post-install). Pure derivation
// from an Entry — no I/O — so it's fully unit-testable ahead of the agent wiring.
type CreateSpec struct {
	AppType      string   // wsgi | asgi
	Server       string   // gunicorn | uvicorn | hypercorn | daphne
	Entrypoint   string   // rendered module:callable (e.g. site.wsgi:application)
	Requirements []string // pinned pip deps written to requirements.txt
	Project      string   // scaffold project name (cmd scaffolds)

	ScaffoldKind    string            // cmd | template
	ScaffoldCommand []string          // argv for a cmd scaffold (rendered); nil for template
	TemplateFiles   map[string]string // relpath->content for a template scaffold; nil for cmd

	// HardenSettings requests the Django-style settings.py rewrite (SECRET_KEY +
	// ALLOWED_HOSTS from env, STATIC_ROOT). Derived from a secret_key generator.
	HardenSettings bool
	// PatchScript, when set, is the framework's own python settings surgery run
	// in place of the generic hardener (Wagtail/Channels). Takes precedence.
	PatchScript string

	NeedsDB     string
	StaticURL   string
	StaticRoot  string
	MediaURL    string
	MediaRoot   string
	PostInstall []string
	// GenerateEnv are the env vars the scaffolder must mint a value for (secret
	// generators) — kept separate so the caller writes them to the app's env,
	// never to source.
	GenerateEnv []EnvVar
	// Env is the full declared default env (generated + static). The create flow
	// merges these into the app's env after scaffolding, filling generated keys
	// from the scaffolder's output.
	Env []EnvVar
}

var (
	// projectRe guards a cmd-scaffold project name — it becomes a Python package
	// (django-admin startproject <name>), so it must be an importable identifier.
	projectRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	// entrypointRe mirrors the runtime's module:callable contract after rendering.
	entrypointRe = regexp.MustCompile(`^[A-Za-z0-9_.]+:[A-Za-z0-9_]+$`)
)

// renderProject substitutes {{.Project}} in a template with the project name.
// Only that one variable is supported, so a literal replace is enough (and
// avoids pulling text/template + its error surface into the hot path).
func renderProject(tmpl, project string) string {
	return strings.ReplaceAll(tmpl, "{{.Project}}", project)
}

// ResolveCreate turns a validated Entry into the runtime CreateSpec. It renders
// the entrypoint + scaffold command against the project name and re-checks the
// derived entrypoint against the runtime's module:callable contract, so a bad
// template can't produce an unlaunchable app.
func (e Entry) ResolveCreate() (CreateSpec, error) {
	project := e.Scaffold.Project
	if project == "" {
		project = "app"
	}
	if e.Scaffold.Kind == "cmd" && !projectRe.MatchString(project) {
		return CreateSpec{}, fmt.Errorf("scaffold.project %q: must be an importable identifier ^[a-z][a-z0-9_]{0,31}$", project)
	}

	entrypoint := renderProject(e.EntrypointTemplate, project)
	if !entrypointRe.MatchString(entrypoint) {
		return CreateSpec{}, fmt.Errorf("rendered entrypoint %q is not module:callable", entrypoint)
	}

	spec := CreateSpec{
		AppType:      e.AppType,
		Server:       e.Server,
		Entrypoint:   entrypoint,
		Requirements: append([]string(nil), e.PipRequirements...),
		Project:      project,
		ScaffoldKind: e.Scaffold.Kind,
		NeedsDB:      e.NeedsDB,
		StaticURL:    e.StaticURL,
		StaticRoot:   e.StaticRoot,
		MediaURL:     e.MediaURL,
		MediaRoot:    e.MediaRoot,
		PostInstall:  append([]string(nil), e.PostInstall...),
	}
	if e.Scaffold.Kind == "cmd" {
		spec.ScaffoldCommand = strings.Fields(renderProject(e.Scaffold.Command, project))
		if len(spec.ScaffoldCommand) == 0 {
			return CreateSpec{}, fmt.Errorf("scaffold.command rendered empty")
		}
	} else {
		spec.TemplateFiles = e.TemplateFiles
	}
	spec.PatchScript = e.PatchScript
	spec.Env = append([]EnvVar(nil), e.DefaultEnv...)
	for _, v := range e.DefaultEnv {
		if v.Generate != "" {
			spec.GenerateEnv = append(spec.GenerateEnv, v)
		}
		if v.Generate == "secret_key" {
			spec.HardenSettings = true
		}
	}
	return spec, nil
}
