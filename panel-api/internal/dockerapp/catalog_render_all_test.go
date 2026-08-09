package dockerapp

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRender_AllApps_ValidYAML renders every shipped catalog entry with its
// generated secrets materialised and one runtime port per declared port, then
// asserts the compose renders without error, leaves no unresolved template var
// ("<no value>"), and parses as well-formed YAML. A template typo (bad field,
// missing func, malformed port block) in ANY app.yaml/compose.yml.tmpl fails
// here in CI instead of at a first-install 500. Complements the SMTP-only
// render tests, which skip non-smtp apps like pgadmin.
func TestRender_AllApps_ValidYAML(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	if cat.Len() == 0 {
		t.Fatal("catalog loaded zero entries")
	}
	for _, e := range cat.All() {
		slug := e.Slug
		env, err := MaterialiseEnv(e, nil)
		if err != nil {
			t.Errorf("%s MaterialiseEnv: %v", slug, err)
			continue
		}
		ports := make(map[string]RuntimePort, len(e.Ports))
		for i, p := range e.Ports {
			ports[p.Name] = RuntimePort{
				HostPort:      20000 + i,
				ContainerPort: p.ContainerPort,
				BindInterface: "127.0.0.1",
				Protocol:      p.Protocol,
			}
		}
		out, err := Render(e, RenderParams{
			Slug: slug, Name: "t", Domain: slug + ".example.com",
			ImageChannel: e.ImageChannel, DataRoot: "/var/lib/jabali/docker-apps/" + slug,
			CPULimit: "1.0", MemoryLimit: "1g", PIDsLimit: 200, Ports: ports, Env: env,
		})
		if err != nil {
			t.Errorf("%s Render: %v", slug, err)
			continue
		}
		if strings.Contains(out, "<no value>") {
			t.Errorf("%s: unresolved template var:\n%s", slug, out)
		}
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(out), &node); err != nil {
			t.Errorf("%s: render produced INVALID YAML: %v\n%s", slug, err, out)
		}
	}
}
