package dockerapp

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Fixes from installing every catalog app on a test host (2026-10-20).

// Plausible exits at boot unless TOTP_VAULT_KEY is standard base64 of exactly
// 32 bytes ("TOTP_VAULT_KEY must be Base64 encoded 32 bytes"); password64 gave
// 64 URL-safe characters of 48 bytes.
func TestPlausible_TOTPVaultKeyIsBase64Of32Bytes(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	p, ok := cat.Get("plausible")
	if !ok {
		t.Fatal("plausible missing from catalog")
	}
	env, err := MaterialiseEnv(p, nil)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(env["TOTP_VAULT_KEY"])
	if err != nil || len(raw) != 32 {
		t.Fatalf("TOTP_VAULT_KEY %q decodes to %d bytes (err %v), want 32", env["TOTP_VAULT_KEY"], len(raw), err)
	}
}

func TestGenerateSecret_EverySchemeTheLoaderAccepts(t *testing.T) {
	for scheme := range secretSchemes {
		if _, err := generateSecret(scheme); err != nil {
			t.Errorf("%s: %v", scheme, err)
		}
	}
}

// An unknown generate: scheme is refused when the catalog loads, not when an
// install fails on it.
func TestEntry_Validate_RejectsUnknownGenerateScheme(t *testing.T) {
	e := Entry{
		Slug:         "demo",
		Name:         "x",
		Version:      "0.1",
		Description:  "x",
		ImageChannel: "x:1@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Volumes:      []Volume{{Name: "data", ContainerPath: "/data"}},
		Ports: []PortSpec{
			{Name: "http", ContainerPort: 80, Protocol: "tcp", DefaultEnabled: true, DefaultBind: "loopback", DefaultReverseProxy: true},
		},
		Env: []EnvVar{{Name: "KEY", Secret: true, Generate: "base64_33"}},
	}
	if err := e.validate(); err == nil || !strings.Contains(err.Error(), "base64_33") {
		t.Fatalf("want the unknown scheme refused, got %v", err)
	}
	e.Env[0].Generate = "base64_32"
	if err := e.validate(); err != nil {
		t.Fatalf("base64_32 refused: %v", err)
	}
}

// Each of these exited on an install without a domain: Ghost
// ERR_INVALID_URL, PeerTube "https://undefined", Vaultwarden "empty host",
// ZITADEL "admin@" missing its domain, InvoiceShelf "Invalid URI".
func TestCatalog_AppsThatNeedADomainSaySo(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	for _, slug := range []string{"ghost", "peertube", "vaultwarden", "zitadel", "invoiceshelf"} {
		e, ok := cat.Get(slug)
		if !ok {
			t.Errorf("%s missing from catalog", slug)
			continue
		}
		if !e.RequiresDomain {
			t.Errorf("%s: requires_domain is not set", slug)
		}
	}
}

// The registry stopped serving this image, so no install of the app could
// start. It may not come back into the catalog.
func TestCatalog_NoImageFromARegistryThatStoppedServingIt(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	for _, e := range cat.All() {
		for _, gone := range []string{"gcr.io/zenika-hub/alpine-chrome"} {
			if strings.Contains(e.ComposeTemplate(), "image: "+gone) {
				t.Errorf("%s still uses %s", e.Slug, gone)
			}
		}
	}
}

func TestFailureSummary_KeepsTheComposeError(t *testing.T) {
	msg := "agent: internal: docker compose up -d failed: exit status 1\n" +
		" Image gcr.io/zenika-hub/alpine-chrome:124 Interrupted\n" +
		"Error response from daemon: Get \"https://gcr.io/v2/zenika-hub/alpine-chrome/manifests/124\": denied: This API method requires billing to be enabled.\n" +
		"Error response from daemon: No such image: getmeili/meilisearch:v1.41.0"
	got := FailureSummary(msg)
	want := "agent: internal: docker compose up -d failed: exit status 1: Error response from daemon: Get \"https://gcr.io/v2/zenika-hub/alpine-chrome/manifests/124\": denied: This API method requires billing to be enabled."
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if got := FailureSummary("agent: timeout"); got != "agent: timeout" {
		t.Fatalf("one-line message changed: %q", got)
	}
	if got := FailureSummary("first\n Image x Pulling\n"); got != "first" {
		t.Fatalf("no error line: got %q, want the first line", got)
	}
	long := "first\nError: " + strings.Repeat("é", 400)
	if got := FailureSummary(long); len([]rune(got)) != len([]rune("first: "))+300+1 {
		t.Fatalf("long line not cut at 300 characters: %d", len([]rune(got)))
	}
}

func renderDrill(t *testing.T, slug string) string {
	t.Helper()
	cat, _ := LoadDir(repoCatalogDir(t))
	e, ok := cat.Get(slug)
	if !ok {
		t.Fatalf("%s missing from catalog", slug)
	}
	env, err := MaterialiseEnv(e, nil)
	if err != nil {
		t.Fatalf("%s materialise: %v", slug, err)
	}
	out, err := Render(e, RenderParams{
		Slug: slug, Name: "drill", Domain: "drill.example.com",
		ImageChannel: e.ImageChannel,
		DataRoot:     "/var/lib/jabali/docker-apps/" + slug,
		CPULimit:     "1", MemoryLimit: "1g",
		Ports: map[string]RuntimePort{"http": {HostPort: 10091, ContainerPort: 80, BindInterface: "127.0.0.1", Protocol: "tcp"}},
		Env:   env,
	})
	if err != nil {
		t.Fatalf("%s render: %v", slug, err)
	}
	return out
}

// ClickHouse 25 refuses the passwordless default user over the network, so
// Plausible exited on "Authentication failed". ClickHouse gets a generated
// password, and Plausible's URL carries it.
func TestPlausible_ClickHouseLoginHasAPassword(t *testing.T) {
	out := renderDrill(t, "plausible")
	m := regexp.MustCompile(`CLICKHOUSE_PASSWORD: "([^"]+)"`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("clickhouse has no password:\n%s", out)
	}
	mustContain(t, out, `CLICKHOUSE_DATABASE_URL: "http://default:`+m[1]+`@clickhouse:8123/plausible_events_db"`)
}

// The bind mount over sites/ hid sqlconf.php, so OpenEMR never ran its setup.
// A one-shot step copies the image's sites/ into the mount first.
func TestOpenEMR_SitesAreSeededBeforeTheAppStarts(t *testing.T) {
	out := renderDrill(t, "openemr")
	var doc struct {
		Services map[string]struct {
			Volumes   []string                  `yaml:"volumes"`
			DependsOn map[string]map[string]any `yaml:"depends_on"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	seed, ok := doc.Services["init-seed"]
	if !ok || len(seed.Volumes) != 1 || seed.Volumes[0] != "/var/lib/jabali/docker-apps/openemr/sites:/seed" {
		t.Fatalf("init-seed missing or mounts %v", seed.Volumes)
	}
	mustContain(t, out, "tar xf - --skip-old-files -C /seed && chown apache:root /seed && chmod 0755 /seed")
	if c := doc.Services["openemr"].DependsOn["init-seed"]["condition"]; c != "service_completed_successfully" {
		t.Fatalf("openemr depends on init-seed with %v", c)
	}
}

// These images have no wget, and their health check called it, so the app
// never turned healthy. Each one now runs a tool its image has.
func TestCatalog_HealthChecksDoNotCallWgetWhereTheImageHasNone(t *testing.T) {
	for slug, services := range map[string][]string{
		"freshrss":      {"freshrss"},
		"uptime-kuma":   {"uptime-kuma"},
		"mediacms":      {"mediacms"},
		"immich":        {"immich-server", "immich-machine-learning"},
		"vaultwarden":   {"vaultwarden"},
		"joplin":        {"joplin"},
		"mealie":        {"mealie"},
		"paperless-ngx": {"paperless-ngx"},
	} {
		var doc struct {
			Services map[string]struct {
				Healthcheck struct {
					Test []string `yaml:"test"`
				} `yaml:"healthcheck"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(renderDrill(t, slug)), &doc); err != nil {
			t.Fatalf("%s parse: %v", slug, err)
		}
		for _, svc := range services {
			s, ok := doc.Services[svc]
			if !ok {
				t.Errorf("%s: no service %q", slug, svc)
				continue
			}
			if test := strings.Join(s.Healthcheck.Test, " "); test == "" || strings.Contains(test, "wget") {
				t.Errorf("%s/%s health check %q", slug, svc, test)
			}
		}
	}
}

// Joplin answers 404 "Invalid origin" to a probe whose Host is not
// APP_BASE_URL's, so the app never turned healthy.
func TestJoplin_ProbeSendsTheSiteHost(t *testing.T) {
	mustContain(t, renderDrill(t, "joplin"), "headers: {Host: 'drill.example.com'}")
}

// Plausible started before ClickHouse answered and exited once.
func TestPlausible_WaitsForItsDatabases(t *testing.T) {
	var doc struct {
		Services map[string]struct {
			DependsOn map[string]map[string]any `yaml:"depends_on"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(renderDrill(t, "plausible")), &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, db := range []string{"postgres", "clickhouse"} {
		if c := doc.Services["plausible"].DependsOn[db]["condition"]; c != "service_healthy" {
			t.Errorf("plausible depends on %s with %v", db, c)
		}
	}
}
