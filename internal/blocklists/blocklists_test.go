package blocklists

import (
	"slices"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	got, err := Normalize([]string{"firehol_level3", " firehol_level1 ", "firehol_level3", ""})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"firehol_level1", "firehol_level3"}; !slices.Equal(got, want) {
		t.Fatalf("Normalize = %v, want %v (catalog order, no repeats)", got, want)
	}
	if got, err := Normalize(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("Normalize(nil) = %v, %v; want an empty, non-nil list", got, err)
	}
	if _, err := Normalize([]string{"firehol_level1", "https://evil.example/list"}); err == nil {
		t.Fatal("Normalize accepted a key that is not in the catalog")
	}
}

// The agent fetches only these URLs, so each must be https.
func TestCatalogURLsAreHTTPS(t *testing.T) {
	seen := map[string]bool{}
	for _, l := range Catalog {
		if !strings.HasPrefix(l.URL, "https://") {
			t.Errorf("%s: URL %q is not https", l.Key, l.URL)
		}
		if seen[l.Key] {
			t.Errorf("key %q appears twice", l.Key)
		}
		seen[l.Key] = true
		if Reason(l.Key) != "jabali-list:"+l.Key {
			t.Errorf("Reason(%q) = %q", l.Key, Reason(l.Key))
		}
	}
}
