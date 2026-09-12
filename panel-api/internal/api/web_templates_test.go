package api

import "testing"

// GH #1624 / ADR-0169 Phase 3: buildWebTemplate validates admin web-template
// input. Directives run through the SAME relaxed denylist the admin
// custom-directives PATCH uses (ValidateNginxDirectivesAdmin).
func TestBuildWebTemplate(t *testing.T) {
	t.Run("name required", func(t *testing.T) {
		_, _, code, _ := buildWebTemplate(webTemplateInput{NginxDirectives: "add_header X-A b;"})
		if code != "name_required" {
			t.Fatalf("code = %q, want name_required", code)
		}
	})

	t.Run("directives required", func(t *testing.T) {
		_, _, code, _ := buildWebTemplate(webTemplateInput{Name: "t"})
		if code != "directives_required" {
			t.Fatalf("code = %q, want directives_required", code)
		}
	})

	t.Run("denylisted directive rejected", func(t *testing.T) {
		_, _, code, _ := buildWebTemplate(webTemplateInput{Name: "t", NginxDirectives: "root /etc/jabali-panel/;"})
		if code != "invalid_directives" {
			t.Fatalf("code = %q, want invalid_directives", code)
		}
	})

	// proxy_pass is NOT in the admin denylist — this is exactly why web templates
	// are admin-select-only: a globally-visible template holding this, if a tenant
	// could pick it, would front a localhost service from the tenant's own domain.
	t.Run("proxy_pass accepted (admin denylist is relaxed)", func(t *testing.T) {
		_, status, code, _ := buildWebTemplate(webTemplateInput{Name: "t", NginxDirectives: "proxy_pass http://127.0.0.1:9000;"})
		if status != 0 {
			t.Fatalf("proxy_pass rejected (code=%q); admin path allows it", code)
		}
	})

	t.Run("valid template — trimmed + ID assigned", func(t *testing.T) {
		tmpl, status, _, _ := buildWebTemplate(webTemplateInput{Name: "  WordPress  ", Description: "wp", NginxDirectives: "  add_header X-A b;  "})
		if status != 0 {
			t.Fatalf("valid template rejected, status = %d", status)
		}
		if tmpl.Name != "WordPress" {
			t.Errorf("name = %q, want trimmed WordPress", tmpl.Name)
		}
		if tmpl.NginxDirectives != "add_header X-A b;" {
			t.Errorf("directives = %q, want trimmed", tmpl.NginxDirectives)
		}
		if tmpl.ID == "" {
			t.Error("no ID assigned")
		}
	})
}
