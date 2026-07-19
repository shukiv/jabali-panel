package pyframeworks

import (
	"path/filepath"
	"testing"
)

// catalogRoot is the in-repo framework catalog, relative to this package.
const catalogRoot = "../../../install/py-frameworks"

// TestCatalogLoad_ValidatesAllEntries walks the real catalog and asserts every
// framework.yaml parses + passes validation (no skipped entries). This is the
// JAB-164 counterpart of the docker-app TestCatalogLoad — a malformed entry
// fails CI instead of being silently dropped at boot.
func TestCatalogLoad_ValidatesAllEntries(t *testing.T) {
	c, errs := LoadDir(catalogRoot)
	for _, e := range errs {
		t.Errorf("catalog entry %s failed to load: %v", e.Slug, e.Err)
	}
	if c.Len() == 0 {
		t.Fatal("catalog loaded zero entries — expected the seeded frameworks")
	}
	// The three seed entries must be present and typed correctly.
	for slug, wantType := range map[string]string{"flask": AppTypeWSGI, "fastapi": AppTypeASGI, "django": AppTypeWSGI} {
		e, ok := c.Get(slug)
		if !ok {
			t.Errorf("expected %q in the catalog", slug)
			continue
		}
		if e.AppType != wantType {
			t.Errorf("%s app_type = %q, want %q", slug, e.AppType, wantType)
		}
		if len(e.PipRequirements) == 0 {
			t.Errorf("%s: pip_requirements empty", slug)
		}
	}
	// Listing order is popularity-desc: Django (95) before Flask (90) before FastAPI (88).
	all := c.All()
	if all[0].Slug != "django" {
		t.Errorf("most-popular entry = %q, want django", all[0].Slug)
	}
}

func TestValidate_RejectsServerTypeMismatch(t *testing.T) {
	base := func() Entry {
		return Entry{
			Slug: "testfw", Name: "X", Version: "1", Description: "d",
			AppType: AppTypeWSGI, Server: "gunicorn",
			PipRequirements:    []string{"x==1"},
			Scaffold:           Scaffold{Kind: "template"},
			TemplateFiles:      map[string]string{"app.py": "app = object()\n"},
			EntrypointTemplate: "app:app",
		}
	}
	if err := base().validate(); err != nil {
		t.Fatalf("valid WSGI entry rejected: %v", err)
	}
	// ASGI app declaring a WSGI server must fail.
	bad := base()
	bad.AppType = AppTypeASGI
	bad.Server = "gunicorn"
	if err := bad.validate(); err == nil {
		t.Error("ASGI entry with gunicorn must be rejected")
	}
	// Unknown needs_db must fail.
	db := base()
	db.NeedsDB = "oracle"
	if err := db.validate(); err == nil {
		t.Error("needs_db=oracle must be rejected")
	}
	// Half a static split must fail.
	st := base()
	st.StaticURL = "/static/"
	if err := st.validate(); err == nil {
		t.Error("static_url without static_root must be rejected")
	}
}

func TestLoadDir_MissingRoot(t *testing.T) {
	c, errs := LoadDir(filepath.Join(t.TempDir(), "nope"))
	if len(errs) == 0 {
		t.Error("missing root should report an error")
	}
	if c.Len() != 0 {
		t.Error("missing root should yield an empty catalog")
	}
}
