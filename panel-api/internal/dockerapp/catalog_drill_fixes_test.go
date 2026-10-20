package dockerapp

import (
	"encoding/base64"
	"strings"
	"testing"
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
