package models

import "testing"

func TestPreviewNaming(t *testing.T) {
	if got := PreviewSlug("shop.example.com"); got != "shop-example-com" {
		t.Errorf("slug = %q", got)
	}
	if got := PreviewSlug("Example.COM."); got != "example-com" {
		t.Errorf("slug must lowercase + strip trailing dot, got %q", got)
	}
	if got := PreviewHost("example.com", "host.tld"); got != "example-com.preview.host.tld" {
		t.Errorf("host = %q", got)
	}
	if got := PreviewHost("example.com", ""); got != "" {
		t.Errorf("no hostname must yield empty preview host, got %q", got)
	}
	if got := PreviewWildcardName("host.tld"); got != "*.preview.host.tld" {
		t.Errorf("wildcard = %q", got)
	}
}

// The flattening is not injective — this pins the collision pair the
// enable handler guards against.
func TestPreviewSlugCollision(t *testing.T) {
	if PreviewSlug("my-site.com") != PreviewSlug("my.site.com") {
		t.Error("expected my-site.com and my.site.com to collide (that is why the handler checks)")
	}
}
