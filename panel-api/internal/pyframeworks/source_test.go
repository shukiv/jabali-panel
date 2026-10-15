package pyframeworks

import (
	"reflect"
	"strings"
	"testing"
)

// GH #2156: a "source" entry installs a full upstream app (sic) instead of a
// framework starter. Its code is a GitHub commit tarball checked against a
// pinned sha256, and it may name shared libraries the agent installs.

const (
	sicURL = "https://codeload.github.com/epilys/sic/tar.gz/9bef6edad2f78e93265739969e1974236127ee7d"
	sicSHA = "30d334b170115bb5346c6f974a7a40175630fa1dee8c7b01d590a92c8b7fe926"
)

func sourceEntry() Entry {
	return Entry{
		Slug: "sic", Name: "sic", Version: "9bef6ed", Description: "d",
		AppType: AppTypeWSGI, Server: "gunicorn",
		PipRequirements:    []string{"Django==6.0.3", "gunicorn==23.0.0"},
		Scaffold:           Scaffold{Kind: "source", URL: sicURL, SHA256: sicSHA, Project: "sic"},
		EntrypointTemplate: "{{.Project}}.wsgi:application",
		SystemLibs:         []string{"magickwand"},
	}
}

func TestValidate_SourceScaffold(t *testing.T) {
	if err := sourceEntry().validate(); err != nil {
		t.Fatalf("valid source entry rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Entry){
		"no url":     func(e *Entry) { e.Scaffold.URL = "" },
		"plain http": func(e *Entry) { e.Scaffold.URL = strings.Replace(sicURL, "https://", "http://", 1) },
		"other host": func(e *Entry) { e.Scaffold.URL = strings.Replace(sicURL, "codeload.github.com", "example.com", 1) },
		"host with port": func(e *Entry) {
			e.Scaffold.URL = strings.Replace(sicURL, "codeload.github.com", "codeload.github.com:8443", 1)
		},
		"userinfo":          func(e *Entry) { e.Scaffold.URL = strings.Replace(sicURL, "https://", "https://u:p@", 1) },
		"branch not commit": func(e *Entry) { e.Scaffold.URL = "https://codeload.github.com/epilys/sic/tar.gz/main" },
		"query":             func(e *Entry) { e.Scaffold.URL = sicURL + "?x=1" },
		"no sha256":         func(e *Entry) { e.Scaffold.SHA256 = "" },
		"short sha256":      func(e *Entry) { e.Scaffold.SHA256 = sicSHA[:63] },
		"uppercase sha256":  func(e *Entry) { e.Scaffold.SHA256 = strings.ToUpper(sicSHA) },
		"no project":        func(e *Entry) { e.Scaffold.Project = "" },
		"bad project":       func(e *Entry) { e.Scaffold.Project = "../sic" },
		"unknown lib":       func(e *Entry) { e.SystemLibs = []string{"libfoo"} },
		"lib twice":         func(e *Entry) { e.SystemLibs = []string{"magickwand", "magickwand"} },
	} {
		e := sourceEntry()
		mutate(&e)
		if err := e.validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, e.Scaffold)
		}
	}
}

// system_libs isn't tied to kind=source: a starter may need a library too.
func TestValidate_SystemLibsOnAStarter(t *testing.T) {
	e := Entry{
		Slug: "flaskimg", Name: "X", Version: "1", Description: "d",
		AppType: AppTypeWSGI, Server: "gunicorn",
		PipRequirements:    []string{"flask==3"},
		Scaffold:           Scaffold{Kind: "template"},
		TemplateFiles:      map[string]string{"app.py": "app = object()\n"},
		EntrypointTemplate: "app:app",
		SystemLibs:         []string{"magickwand"},
	}
	if err := e.validate(); err != nil {
		t.Fatalf("starter with a known system lib rejected: %v", err)
	}
	spec, err := e.ResolveCreate()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spec.SystemLibs, []string{"magickwand"}) {
		t.Fatalf("SystemLibs = %v", spec.SystemLibs)
	}
}

func TestResolveCreate_SourceScaffold(t *testing.T) {
	spec, err := sourceEntry().ResolveCreate()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if spec.ScaffoldKind != "source" || spec.SourceURL != sicURL || spec.SourceSHA256 != sicSHA {
		t.Fatalf("source spec = kind %q url %q sha %q", spec.ScaffoldKind, spec.SourceURL, spec.SourceSHA256)
	}
	if spec.Project != "sic" || spec.Entrypoint != "sic.wsgi:application" {
		t.Fatalf("project %q entrypoint %q", spec.Project, spec.Entrypoint)
	}
	if spec.ScaffoldCommand != nil || spec.TemplateFiles != nil {
		t.Fatalf("a source spec carries a command %v or templates %v", spec.ScaffoldCommand, spec.TemplateFiles)
	}
	if !reflect.DeepEqual(spec.SystemLibs, []string{"magickwand"}) {
		t.Fatalf("SystemLibs = %v", spec.SystemLibs)
	}
}
