package dockerapp

import (
	"strings"
	"testing"
)

// Regression for the n8n install failure on production: the install flow
// materialises catalog env defaults (N8N_HOST, WEBHOOK_URL) into .Env,
// and the template ALSO hardcodes those keys — both were emitted, docker
// compose refused the duplicate mapping keys, and the install died as an
// opaque "docker compose pull failed: exit status 1".
func TestRender_N8N_NoDuplicateEnvKeys(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	a, ok := cat.Get("n8n")
	if !ok {
		t.Fatal("n8n missing from catalog")
	}
	out, err := Render(a, RenderParams{
		Slug: "n8n", Name: "aaautomation", Domain: "auto.example.com",
		ImageChannel: "n8nio/n8n:2.28.2",
		DataRoot:     "/var/lib/jabali/docker-apps/n8n-aaautomation",
		CPULimit:     "2.0", MemoryLimit: "1024",
		Ports: map[string]RuntimePort{"http": {HostPort: 10000, ContainerPort: 5678, BindInterface: "127.0.0.1", Protocol: "tcp"}},
		// Exactly the .Env shape from the failed production install.
		Env: map[string]string{
			"N8N_HOST":         "auto.example.com",
			"WEBHOOK_URL":      "https://auto.example.com/",
			"GENERIC_TIMEZONE": "Asia/Jerusalem",
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, key := range []string{"N8N_HOST:", "WEBHOOK_URL:", "N8N_EDITOR_BASE_URL:"} {
		if got := strings.Count(out, key); got != 1 {
			t.Errorf("%s appears %d times, want exactly 1:\n%s", key, got, out)
		}
	}
	// The template-owned values win; the pass-through key survives.
	if !strings.Contains(out, `N8N_HOST: "0.0.0.0"`) {
		t.Error("template-owned N8N_HOST value must win")
	}
	if !strings.Contains(out, `GENERIC_TIMEZONE: "Asia/Jerusalem"`) {
		t.Error("non-conflicting env keys must still pass through")
	}
	// The bare "1024" memory limit means BYTES to compose — must be
	// normalised to megabytes.
	if !strings.Contains(out, "memory: 1024m") {
		t.Error("unitless memory limit must render with an m suffix")
	}
}

func TestRender_Mealie_NoDuplicateBaseURL(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	a, ok := cat.Get("mealie")
	if !ok {
		t.Skip("mealie not in catalog")
	}
	out, err := Render(a, RenderParams{
		Slug: "mealie", Name: "recipes", Domain: "food.example.com",
		ImageChannel: a.ImageChannel,
		DataRoot:     "/var/lib/jabali/docker-apps/mealie-recipes",
		CPULimit:     "1.0", MemoryLimit: "512m",
		Ports: map[string]RuntimePort{"http": {HostPort: 10001, ContainerPort: 9000, BindInterface: "127.0.0.1", Protocol: "tcp"}},
		Env:   map[string]string{"BASE_URL": "https://food.example.com"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := strings.Count(out, "BASE_URL:"); got != 1 {
		t.Errorf("BASE_URL appears %d times, want exactly 1", got)
	}
}

// The render-time YAML probe turns any future duplicate-key template bug
// into a clear error instead of a failed install.
func TestRenderRejectsDuplicateKeysAtRenderTime(t *testing.T) {
	e := Entry{
		Slug: "duptest",
		composeTmpl: `services:
  app:
    image: x
    environment:
      A: "1"
      A: "2"
`,
	}
	if _, err := Render(e, RenderParams{}); err == nil {
		t.Fatal("expected a render error for duplicate mapping keys")
	} else if !strings.Contains(err.Error(), "not valid YAML") {
		t.Errorf("error should name the YAML problem, got: %v", err)
	}
}

func TestNormalizeMemoryLimit(t *testing.T) {
	cases := map[string]string{
		"1024":  "1024m",
		"512m":  "512m",
		"1g":    "1g",
		"":      "",
		" 256 ": "256m",
	}
	for in, want := range cases {
		if got := normalizeMemoryLimit(in); got != want {
			t.Errorf("normalizeMemoryLimit(%q) = %q, want %q", in, got, want)
		}
	}
}
