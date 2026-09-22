package main

import (
	"reflect"
	"testing"
)

// JAB-366: parseUIDList feeds the main agent socket's SO_PEERCRED allow-list.
// install.sh builds it as "<panel_uid>,0". It skips blanks and junk WITHIN a
// list (dropping a bad token only restricts the allow-list); whether an empty
// result is tolerated is decided by decideUIDGate, tested below.
func TestParseUIDList(t *testing.T) {
	cases := []struct {
		in   string
		want []uint32
	}{
		{"1001,0", []uint32{1001, 0}},
		{"", nil},
		{"   ", nil},
		{" 5 , , 7 ", []uint32{5, 7}},
		{"abc,9", []uint32{9}}, // junk skipped, not fatal
		{"0", []uint32{0}},
		{"-1,3", []uint32{3}}, // negative is not a valid uint32 → skipped
	}
	for _, c := range cases {
		got := parseUIDList(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseUIDList(%q) = %v; want %v", c.in, got, c.want)
		}
	}
}

// JAB-357 fail-open hardening: decideUIDGate must never yield an empty allow-list
// with a nil error unless the operator explicitly opted out. A mangled or unset
// -allowed-uids used to silently disable the SO_PEERCRED gate and serve the root
// Agent socket to any local UID (e.g. a regressed webmail service account).
func TestDecideUIDGate(t *testing.T) {
	cases := []struct {
		name             string
		raw              string
		insecureAllowAny bool
		want             []uint32
		wantErr          bool
	}{
		// Prod path (install.sh passes panel_uid,0) — unchanged, gate active.
		{"prod list", "1001,0", false, []uint32{1001, 0}, false},
		{"root only", "0", false, []uint32{0}, false},
		// A bad token inside a valid list only restricts the allow-list → kept.
		{"partial junk stays active", "1001,xyz", false, []uint32{1001}, false},
		// Unset/blank without the opt-out is fatal, not fail-open.
		{"unset is fatal", "", false, nil, true},
		{"blank is fatal", "   ", false, nil, true},
		{"commas only is fatal", " , , ", false, nil, true},
		// Explicit opt-out disables the gate (test runs only).
		{"unset with opt-out ok", "", true, nil, false},
		// All-malformed is fatal EVEN WITH the opt-out: garbage is never a
		// deliberate "open", and the operator clearly intended a gate. (Load-bearing.)
		{"all junk is fatal despite opt-out", "abc,xyz", true, nil, true},
		{"all junk is fatal", "abc,xyz", false, nil, true},
		{"lone bad uid is fatal", "-1", false, nil, true},
	}
	for _, c := range cases {
		got, err := decideUIDGate(c.raw, c.insecureAllowAny)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: decideUIDGate(%q, %v) err = %v; wantErr %v", c.name, c.raw, c.insecureAllowAny, err, c.wantErr)
		}
		if !c.wantErr && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: decideUIDGate(%q, %v) = %v; want %v", c.name, c.raw, c.insecureAllowAny, got, c.want)
		}
	}
}

// JAB-357 AC4: decideAdminUIDs resolves which peers may assert admin_root. Like
// decideUIDGate it is fatal on all-garbage (garbage is never a deliberate
// grant), but its BLANK case defaults to the connect allow-list rather than
// erroring — a broad allow-list already exists, so the admin File Manager keeps
// working with no extra config. It must NOT widen the set: an explicit list is
// honoured verbatim, and a blank list with an empty connect allow-list yields
// the empty set (no peer may assert admin_root — fail closed).
func TestDecideAdminUIDs(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		allowed []uint32
		want    []uint32
		wantErr bool
	}{
		// Explicit list is honoured verbatim, independent of the connect list.
		{"explicit list", "1001", []uint32{1001, 0}, []uint32{1001}, false},
		{"explicit narrower than connect", "1001", []uint32{1001, 0, 33}, []uint32{1001}, false},
		{"explicit root only", "0", []uint32{1001, 0}, []uint32{0}, false},
		// Unset defaults to the connect allow-list (panel user + root).
		{"unset defaults to connect list", "", []uint32{1001, 0}, []uint32{1001, 0}, false},
		{"blank defaults to connect list", "   ", []uint32{1001, 0}, []uint32{1001, 0}, false},
		{"commas only defaults to connect list", " , , ", []uint32{1001, 0}, []uint32{1001, 0}, false},
		// Unset AND an empty connect list (insecure-allow-any) → empty admin set:
		// no peer may assert admin_root. Fail closed, NOT a wildcard. (Load-bearing.)
		{"unset with empty connect list is empty set", "", nil, nil, false},
		// A partly-junk explicit list only narrows it — the good token is kept.
		{"partial junk stays", "1001,xyz", []uint32{1001, 0}, []uint32{1001}, false},
		// A wholly-malformed explicit list is fatal even though a connect list
		// exists: the operator meant to restrict admin_root and mistyped it; we
		// refuse to silently fall back to the broad connect list. (Load-bearing.)
		{"all junk is fatal despite connect list", "abc,xyz", []uint32{1001, 0}, nil, true},
		{"lone bad uid is fatal", "-1", []uint32{1001, 0}, nil, true},
	}
	for _, c := range cases {
		got, err := decideAdminUIDs(c.raw, c.allowed)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: decideAdminUIDs(%q, %v) err = %v; wantErr %v", c.name, c.raw, c.allowed, err, c.wantErr)
		}
		if !c.wantErr && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: decideAdminUIDs(%q, %v) = %v; want %v", c.name, c.raw, c.allowed, got, c.want)
		}
	}
}
