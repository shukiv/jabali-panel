package models

import "testing"

func TestPreviewNaming(t *testing.T) {
	if got := PreviewSlug("shop.example.com"); got != "shop-example-com" {
		t.Errorf("slug = %q", got)
	}
	if got := PreviewSlug("Example.COM."); got != "example-com" {
		t.Errorf("slug must lowercase + strip trailing dot, got %q", got)
	}
	if got := PreviewHost("example.com", "preview.host.tld"); got != "example-com.preview.host.tld" {
		t.Errorf("host = %q", got)
	}
	if got := PreviewHost("example.com", ""); got != "" {
		t.Errorf("no base must yield empty preview host, got %q", got)
	}
	if got := PreviewWildcardName("preview.host.tld"); got != "*.preview.host.tld" {
		t.Errorf("wildcard = %q", got)
	}
	// GH #836: settings override wins over the derived hostname base.
	if got := EffectivePreviewBase(&ServerSettings{Hostname: "host.tld"}); got != "preview.host.tld" {
		t.Errorf("derived base = %q", got)
	}
	if got := EffectivePreviewBase(&ServerSettings{Hostname: "host.tld", PreviewBase: "*.Preview.Example.COM."}); got != "preview.example.com" {
		t.Errorf("override base must normalize, got %q", got)
	}
	if got := EffectivePreviewBase(&ServerSettings{}); got != "" {
		t.Errorf("no hostname, no base -> empty, got %q", got)
	}
	// JAB-213: a free jabalihosted.com hostname bases previews on the hostname
	// itself (one label deep), so the wildcard A + cert cover them.
	if got := EffectivePreviewBase(&ServerSettings{Hostname: "203-0-113-7.jabalihosted.com"}); got != "203-0-113-7.jabalihosted.com" {
		t.Errorf("free-hostname base = %q, want the hostname itself", got)
	}
	// An explicit preview_base still wins even on a free hostname.
	if got := EffectivePreviewBase(&ServerSettings{Hostname: "203-0-113-7.jabalihosted.com", PreviewBase: "prev.example.com"}); got != "prev.example.com" {
		t.Errorf("explicit base must still win, got %q", got)
	}
}

// The flattening is not injective — this pins the collision pair the
// enable handler guards against.
func TestPreviewSlugCollision(t *testing.T) {
	if PreviewSlug("my-site.com") != PreviewSlug("my.site.com") {
		t.Error("expected my-site.com and my.site.com to collide (that is why the handler checks)")
	}
}
