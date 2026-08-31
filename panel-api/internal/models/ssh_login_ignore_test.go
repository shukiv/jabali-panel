package models

import (
	"reflect"
	"testing"
)

func TestParseSSHIgnoreAccounts(t *testing.T) {
	// commas + newlines + stray whitespace + a duplicate, out of order.
	got := ParseSSHIgnoreAccounts("drfeed, backup\nother\r\n\n  , drfeed ")
	want := []string{"backup", "drfeed", "other"} // trimmed, deduped, sorted
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %v, want %v", got, want)
	}
	if len(ParseSSHIgnoreAccounts("")) != 0 {
		t.Errorf("empty raw must yield empty list")
	}
}

func TestSSHIgnoreSet(t *testing.T) {
	set := SSHIgnoreSet("drfeed, alice")
	if _, ok := set["drfeed"]; !ok {
		t.Error("drfeed missing from set")
	}
	if _, ok := set["bob"]; ok {
		t.Error("bob must not be in set")
	}
}

func TestJoinSSHIgnoreAccounts(t *testing.T) {
	got := JoinSSHIgnoreAccounts([]string{"zeta", "alpha", "alpha", " "})
	if got != "alpha\nzeta" {
		t.Fatalf("Join = %q, want %q", got, "alpha\nzeta")
	}
}
