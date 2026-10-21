package blocklists

import (
	"fmt"
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

func TestNormalizeCustom(t *testing.T) {
	got, err := NormalizeCustom([]string{" https://lists.example.org/drop.txt ", "", "https://lists.example.org/drop.txt", "https://203.0.114.9/l.txt"})
	if err != nil || len(got) != 2 || got[0] != "https://lists.example.org/drop.txt" || got[1] != "https://203.0.114.9/l.txt" {
		t.Fatalf("NormalizeCustom = %v, %v", got, err)
	}
	for _, bad := range []string{
		"http://lists.example.org/drop.txt",
		"ftp://lists.example.org/drop.txt",
		"https://user:pw@lists.example.org/x",
		"https:///nohost",
		"https://localhost/x",
		"https://a.localhost/x",
		"https://127.0.0.1/x",
		"https://10.1.2.3/x",
		"https://192.168.1.1/x",
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/x",
		"https://[fd00::1]/x",
		"https://0.0.0.0/x",
		"https://lists.example.org/a b",
		"https://lists.example.org/" + strings.Repeat("a", 2048),
	} {
		if _, err := NormalizeCustom([]string{bad}); err == nil {
			t.Errorf("NormalizeCustom(%q) accepted", bad)
		}
	}
	many := make([]string, MaxCustom+1)
	for i := range many {
		many[i] = fmt.Sprintf("https://lists.example.org/%d.txt", i)
	}
	if _, err := NormalizeCustom(many); err == nil {
		t.Errorf("%d custom lists accepted, cap is %d", len(many), MaxCustom)
	}
}

func TestCustomKey(t *testing.T) {
	a, b := CustomKey("https://lists.example.org/a.txt"), CustomKey("https://lists.example.org/b.txt")
	if a == b || !IsCustomKey(a) || IsCustomKey("firehol_level1") || a != CustomKey("https://lists.example.org/a.txt") {
		t.Fatalf("CustomKey: %q %q", a, b)
	}
	if _, ok := Lookup(a); ok {
		t.Fatalf("a custom key must not be a catalog key")
	}
}
