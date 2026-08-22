package main

import (
	"reflect"
	"testing"
)

// JAB-366: parseUIDList feeds the main agent socket's SO_PEERCRED allow-list.
// install.sh builds it as "<panel_uid>,0"; it must parse cleanly, skip blanks,
// and ignore junk rather than fail the agent at startup.
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
