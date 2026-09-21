package dockerapp

import "testing"

// Render is the single chokepoint every install / update / rename / CLI door
// funnels through, so it rejects a domain carrying any byte outside the FQDN
// alphabet before it can be substituted unescaped into the compose template
// (GH #1790). A crafted value would otherwise break out of a double-quoted YAML
// scalar and inject sibling keys or an extra service.
func TestRender_RejectsInjectionDomain(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	vw, ok := cat.Get("vaultwarden")
	if !ok {
		t.Fatal("vaultwarden missing from catalog")
	}
	params := func(domain string) RenderParams {
		return RenderParams{
			Slug:         "vaultwarden",
			Name:         "vault-prod",
			Domain:       domain,
			ImageChannel: "vaultwarden/server:latest",
			DataRoot:     "/var/lib/jabali/docker-apps/vaultwarden",
			CPULimit:     "0.5",
			MemoryLimit:  "256m",
			Ports: map[string]RuntimePort{
				"http": {HostPort: 10001, ContainerPort: 80, BindInterface: "127.0.0.1", Protocol: "tcp"},
			},
		}
	}

	bad := []struct {
		name, domain string
	}{
		// The reporter's break-out payload: a quote closes the scalar, a newline
		// starts a new mapping key.
		{"quote+newline breakout", "x\"\n    privileged: true\n    foo: \"y.example.com"},
		// A quote with NO whitespace/HTML/path chars — forces the charset guard,
		// not an incidental whitespace rejection.
		{"bare quote", "evil\"x.example.com"},
		{"colon", "a:b.example.com"},
		{"braces", "x{y}.example.com"},
		{"dollar interp", "x$FOO.example.com"},
	}
	for _, tc := range bad {
		if _, err := Render(vw, params(tc.domain)); err == nil {
			t.Errorf("%s: Render accepted injection domain %q; want rejection", tc.name, tc.domain)
		}
	}

	// A normal FQDN still renders.
	if _, err := Render(vw, params("vault.example.com")); err != nil {
		t.Fatalf("Render rejected a valid domain: %v", err)
	}
	// An empty domain is not an injection (a domain-less render path) and must
	// not be turned into a hard error by the guard.
	if _, err := Render(vw, params("")); err != nil {
		t.Fatalf("Render rejected an empty domain: %v", err)
	}
}
