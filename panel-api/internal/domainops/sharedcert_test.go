package domainops

import (
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// sansPtr builds the *string JSON-array form a shared certificate stores in its
// SANs column, so the tests exercise the exact shape SharedCertCoversHost reads.
func sansPtr(t *testing.T, sans ...string) *string {
	t.Helper()
	b, err := json.Marshal(sans)
	if err != nil {
		t.Fatalf("marshal SANs: %v", err)
	}
	s := string(b)
	return &s
}

// TestHostMatchesSAN pins x509.VerifyHostname wildcard semantics: exact match
// (case/whitespace-insensitive), a leading-label wildcard covering exactly one
// label, and the deliberate non-matches — the apex and a two-label subdomain.
// A cert-coverage matcher is security-adjacent, so each arm is discriminating.
func TestHostMatchesSAN(t *testing.T) {
	cases := []struct {
		name string
		san  string
		host string
		want bool
	}{
		{"exact match", "example.com", "example.com", true},
		{"exact case-insensitive", "Example.COM", "example.com", true},
		{"exact trims space", " example.com ", "example.com", true},
		{"wildcard covers one label", "*.example.com", "sub.example.com", true},
		{"wildcard does not cover apex", "*.example.com", "example.com", false},
		{"wildcard does not cover two labels", "*.example.com", "a.b.example.com", false},
		{"wildcard wrong base", "*.example.com", "sub.other.com", false},
		{"no match", "example.com", "other.com", false},
		{"empty san", "", "example.com", false},
		{"empty host", "example.com", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HostMatchesSAN(c.san, c.host); got != c.want {
				t.Fatalf("HostMatchesSAN(%q, %q) = %v, want %v", c.san, c.host, got, c.want)
			}
		})
	}
}

// TestSharedCertCoversHost pins the JSON-array reader: a nil pointer or
// malformed JSON is "covers nothing" (never read an unreadable SAN list as
// coverage), and any single covering SAN — exact or wildcard — is a match.
func TestSharedCertCoversHost(t *testing.T) {
	good := sansPtr(t, "*.example.com", "example.com")
	bad := "not json"
	cases := []struct {
		name string
		sans *string
		host string
		want bool
	}{
		{"nil is no coverage", nil, "example.com", false},
		{"malformed json is no coverage", &bad, "example.com", false},
		{"exact SAN covers", good, "example.com", true},
		{"wildcard SAN covers", good, "sub.example.com", true},
		{"apex not covered by wildcard-only", sansPtr(t, "*.example.com"), "example.com", false},
		{"host outside all SANs", good, "unrelated.net", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SharedCertCoversHost(c.sans, c.host); got != c.want {
				t.Fatalf("SharedCertCoversHost(%v, %q) = %v, want %v", c.sans, c.host, got, c.want)
			}
		})
	}
}

// TestCoveringSharedCert pins the first-wins selection over the already-filtered
// candidate list: nil when nothing covers, the earliest covering cert when
// several do (so the pick is deterministic), and a match found past a
// non-covering leading entry.
func TestCoveringSharedCert(t *testing.T) {
	id := func(s string) models.SharedCertificate {
		return models.SharedCertificate{ID: s}
	}
	withSANs := func(s string, sans ...string) models.SharedCertificate {
		c := id(s)
		c.SANs = sansPtr(t, sans...)
		return c
	}

	t.Run("empty list is nil", func(t *testing.T) {
		if got := CoveringSharedCert(nil, "example.com"); got != nil {
			t.Fatalf("want nil, got %+v", got)
		}
	})

	t.Run("no covering cert is nil", func(t *testing.T) {
		certs := []models.SharedCertificate{withSANs("a", "other.com"), withSANs("b", "*.other.net")}
		if got := CoveringSharedCert(certs, "example.com"); got != nil {
			t.Fatalf("want nil, got %+v", got)
		}
	})

	t.Run("first covering cert wins", func(t *testing.T) {
		certs := []models.SharedCertificate{
			withSANs("first", "*.example.com"),
			withSANs("second", "sub.example.com"),
		}
		got := CoveringSharedCert(certs, "sub.example.com")
		if got == nil || got.ID != "first" {
			t.Fatalf("want cert %q, got %+v", "first", got)
		}
	})

	t.Run("match found past a non-covering entry", func(t *testing.T) {
		certs := []models.SharedCertificate{
			withSANs("skip", "other.com"),
			withSANs("hit", "example.com"),
		}
		got := CoveringSharedCert(certs, "example.com")
		if got == nil || got.ID != "hit" {
			t.Fatalf("want cert %q, got %+v", "hit", got)
		}
	})
}
