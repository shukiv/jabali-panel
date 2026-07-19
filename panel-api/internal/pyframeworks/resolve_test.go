package pyframeworks

import (
	"reflect"
	"testing"
)

func TestResolveCreate_DjangoCmdScaffold(t *testing.T) {
	c, errs := LoadDir(catalogRoot)
	if len(errs) != 0 {
		t.Fatalf("catalog load errors: %v", errs)
	}
	dj, _ := c.Get("django")
	spec, err := dj.ResolveCreate()
	if err != nil {
		t.Fatalf("resolve django: %v", err)
	}
	if spec.Entrypoint != "config.wsgi:application" {
		t.Errorf("entrypoint = %q, want config.wsgi:application", spec.Entrypoint)
	}
	if want := []string{"django-admin", "startproject", "config", "."}; !reflect.DeepEqual(spec.ScaffoldCommand, want) {
		t.Errorf("scaffold cmd = %v, want %v", spec.ScaffoldCommand, want)
	}
	if spec.NeedsDB != "sqlite" || spec.StaticURL != "/static/" {
		t.Errorf("django db/static wrong: %+v", spec)
	}
	if len(spec.GenerateEnv) != 1 || spec.GenerateEnv[0].Key != "DJANGO_SECRET_KEY" {
		t.Errorf("django must generate DJANGO_SECRET_KEY, got %+v", spec.GenerateEnv)
	}
	if len(spec.PostInstall) != 2 {
		t.Errorf("django post_install = %v, want migrate+collectstatic", spec.PostInstall)
	}
}

func TestResolveCreate_FlaskTemplateScaffold(t *testing.T) {
	c, _ := LoadDir(catalogRoot)
	fl, _ := c.Get("flask")
	spec, err := fl.ResolveCreate()
	if err != nil {
		t.Fatalf("resolve flask: %v", err)
	}
	if spec.Entrypoint != "app:app" {
		t.Errorf("entrypoint = %q, want app:app", spec.Entrypoint)
	}
	if spec.ScaffoldKind != "template" || spec.ScaffoldCommand != nil {
		t.Errorf("template scaffold must have no command, got %v", spec.ScaffoldCommand)
	}
	if spec.Server != "gunicorn" || spec.AppType != AppTypeWSGI {
		t.Errorf("flask server/type wrong: %+v", spec)
	}
}

func TestResolveCreate_RejectsBadProjectAndEntrypoint(t *testing.T) {
	// Non-identifier project name for a cmd scaffold.
	bad := Entry{
		Slug: "bad", AppType: AppTypeWSGI, Server: "gunicorn",
		Scaffold:           Scaffold{Kind: "cmd", Command: "django-admin startproject {{.Project}} .", Project: "1nvalid"},
		EntrypointTemplate: "{{.Project}}.wsgi:application",
	}
	if _, err := bad.ResolveCreate(); err == nil {
		t.Error("bad project name must be rejected")
	}
	// Entrypoint template that renders to a non module:callable.
	badEp := Entry{
		Slug: "badep", AppType: AppTypeWSGI, Server: "gunicorn",
		Scaffold:           Scaffold{Kind: "template"},
		EntrypointTemplate: "not-an-entrypoint",
	}
	if _, err := badEp.ResolveCreate(); err == nil {
		t.Error("non module:callable entrypoint must be rejected")
	}
}
