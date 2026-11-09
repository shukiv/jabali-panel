package repository

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestDefaultIndexTemplateRenders guards the branded domain-default-index
// template: it must parse under html/template (contextual escaping is
// strict inside <style>/<svg>) and expand {{.Domain}}. nginx serves it on
// every site without an index file (GH #2422), so it must not show the
// account's login or document root.
func TestDefaultIndexTemplateRenders(t *testing.T) {
	body := DefaultPageTemplateBody(models.PageTemplateDomainDefaultIndex)
	tm, err := template.New("idx").Parse(body)
	if err != nil {
		t.Fatalf("html/template parse failed: %v", err)
	}
	var b bytes.Buffer
	data := map[string]string{
		"Domain":   "example.com",
		"Username": "alice",
		"DocRoot":  "/home/alice/domains/example.com/public_html",
	}
	if err := tm.Execute(&b, data); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	out := b.String()
	for _, want := range []string{"example.com", "<svg", "</html>"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered index missing %q", want)
		}
	}
	for _, leak := range []string{"alice", "/home/alice"} {
		if strings.Contains(out, leak) {
			t.Errorf("rendered index shows %q", leak)
		}
	}
	if strings.Contains(out, "{{") {
		t.Errorf("rendered index still has an unexpanded {{ placeholder")
	}
}

// TestDefaultErrorTemplatesValid: the static 404/403/500 pages are branded,
// self-contained (inline svg + style), and carry no template actions.
func TestDefaultErrorTemplatesValid(t *testing.T) {
	for _, k := range []string{models.PageTemplateError404, models.PageTemplateError403, models.PageTemplateError500} {
		b := DefaultPageTemplateBody(k)
		if !strings.Contains(b, "<svg") || !strings.Contains(b, "</html>") {
			t.Errorf("%s: missing inline svg or closing html", k)
		}
		if strings.Contains(b, "{{") {
			t.Errorf("%s: error pages must not contain template actions", k)
		}
	}
}
