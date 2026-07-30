package main

import (
	"errors"
	"reflect"
	"testing"
)

// A failed build must be diagnosable: OOM-shaped errors get the memory hint,
// unrelated errors don't.
func TestIsLikelyOOM(t *testing.T) {
	oom := []string{"signal: killed", "fork/exec: cannot allocate memory", "out of memory", "Killed"}
	for _, m := range oom {
		if !isLikelyOOM(errors.New(m)) {
			t.Errorf("%q should read as OOM", m)
		}
	}
	notOOM := []error{nil, errors.New("go build: undefined: foo"), errors.New("permission denied")}
	for _, e := range notOOM {
		if isLikelyOOM(e) {
			t.Errorf("%v should NOT read as OOM", e)
		}
	}
}

func TestShortSHA7(t *testing.T) {
	if got := shortSHA7("ce9d98dbe7c8bae4"); got != "ce9d98d" {
		t.Errorf("shortSHA7 = %q, want ce9d98d", got)
	}
	if got := shortSHA7("abc"); got != "abc" {
		t.Errorf("short input should pass through, got %q", got)
	}
}

// GH #760: the from-source update build must tune down on small hosts and stay
// untouched on real ones. Brackets mirror install.sh:build_backend.
func TestLowRAMGoBuildEnvForMB(t *testing.T) {
	cases := []struct {
		memMB int
		want  []string
	}{
		{0, nil},                                                          // unknown RAM → don't tune
		{-5, nil},                                                         // garbage → don't tune
		{1990, []string{"GOFLAGS=-p=1", "GOMEMLIMIT=900MiB", "GOGC=40"}},  // 2 GB VPS
		{2048, []string{"GOFLAGS=-p=1", "GOMEMLIMIT=900MiB", "GOGC=40"}},  // 2 GB
		{2560, []string{"GOFLAGS=-p=1", "GOMEMLIMIT=900MiB", "GOGC=40"}},  // 2.5 GB edge
		{2561, []string{"GOFLAGS=-p=1", "GOMEMLIMIT=1500MiB", "GOGC=50"}}, // just above
		{3800, []string{"GOFLAGS=-p=1", "GOMEMLIMIT=1500MiB", "GOGC=50"}}, // 3.8 GB
		{4096, []string{"GOFLAGS=-p=1", "GOMEMLIMIT=1500MiB", "GOGC=50"}}, // 4 GB ceiling
		{4097, nil}, // just over → untouched
		{9914, nil}, // big host → untouched
	}
	for _, c := range cases {
		got := lowRAMGoBuildEnvForMB(c.memMB)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("lowRAMGoBuildEnvForMB(%d) = %v, want %v", c.memMB, got, c.want)
		}
	}
}
