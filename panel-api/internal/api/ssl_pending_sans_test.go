package api

import (
	"reflect"
	"testing"
)

// GH #1221: the SSL Manager must show which configured SANs an issued cert does
// not yet cover, so a partial cert (autoconfig/autodiscover dropped for lack of
// public DNS at issuance) doesn't read as a bug.
func TestSANsNotCovered(t *testing.T) {
	base := "example.com"
	desired := []string{"example.com", "www.example.com", "mail.example.com", "autoconfig.example.com", "autodiscover.example.com"}

	// Cert issued with only base + www + mail (autoconfig/autodiscover dropped).
	covered := []string{"example.com", "www.example.com", "mail.example.com"}
	got := sansNotCovered(desired, covered, base)
	want := []string{"autoconfig.example.com", "autodiscover.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending = %v, want %v", got, want)
	}

	// Fully-covered cert → nothing pending. Base counts as covered even if the
	// cert's DNSNames omit it (the agent adds it at issuance).
	if p := sansNotCovered(desired, []string{"www.example.com", "mail.example.com", "autoconfig.example.com", "autodiscover.example.com"}, base); len(p) != 0 {
		t.Fatalf("fully covered should be empty, got %v", p)
	}

	// Case-insensitive match.
	if p := sansNotCovered([]string{"WWW.example.com"}, []string{"www.EXAMPLE.com"}, base); len(p) != 0 {
		t.Fatalf("case-insensitive match failed, got %v", p)
	}
}
