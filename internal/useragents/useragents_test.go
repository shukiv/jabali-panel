package useragents

import (
	"slices"
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	for _, ok := range []string{"python-requests", "Go-http-client/1.1", "Mozilla/5.0 zgrab", "my_bot+v2"} {
		if !Valid(ok) {
			t.Errorf("%q refused, want accepted", ok)
		}
	}
	// Anything that could end the quoted nginx string, the regex or the map
	// line is refused.
	for _, bad := range []string{
		"ab", strings.Repeat("a", 129), `evil"`, "evil;", "a) 1; } server {", "x{y}", "$host", "a\nb", `back\slash`, "(.*)", "tab\there", "",
	} {
		if Valid(bad) {
			t.Errorf("%q accepted, want refused", bad)
		}
	}
}

func TestNormalize(t *testing.T) {
	got, err := Normalize([]string{"  python-requests ", "", "Python-Requests", "zgrab"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"python-requests", "zgrab"}) {
		t.Fatalf("got %v", got)
	}
	if _, err := Normalize([]string{"ok-bot", "bad;bot"}); err == nil {
		t.Fatal("bad entry accepted")
	}
	many := make([]string, MaxBlocked+1)
	for i := range many {
		many[i] = "bot-" + strings.Repeat("x", i%5+1) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := Normalize(many); err == nil {
		t.Fatalf("%d entries accepted, cap is %d", len(many), MaxBlocked)
	}
}
