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
		{0, nil},  // unknown RAM → don't tune
		{-5, nil}, // garbage → don't tune
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

// The updater builds three Go binaries. lowRAMGoBuildEnvForMB caps each at
// GOMEMLIMIT=900MiB, which is a PER-PROCESS ceiling — three at once is 2700MiB
// on a host with under 2 GB. Observed killing `jabali update` on a 2 GB box
// where the same build, run alone straight afterwards, succeeded.
func TestSerializeGoBuildsForMB(t *testing.T) {
	cases := []struct {
		memMB int
		want  bool
		why   string
	}{
		{0, false, "unknown memory must not change behaviour"},
		{1973, true, "the box that actually failed"},
		{2048, true, "a 2 GB VPS cannot host three 900MiB builds"},
		{2560, true, "same threshold as the tight GOMEMLIMIT tier"},
		{2561, false, "above the tight tier, parallel is affordable"},
		{8192, false, "a large host keeps the faster parallel build"},
	}
	for _, c := range cases {
		if got := serializeGoBuildsForMB(c.memMB); got != c.want {
			t.Errorf("serializeGoBuildsForMB(%d) = %v, want %v — %s", c.memMB, got, c.want, c.why)
		}
	}
}

// The serialization threshold must line up with the memory tuning: any host
// getting the aggressive 900MiB per-process cap cannot afford three at once.
func TestSerializeMatchesTightMemLimitTier(t *testing.T) {
	for _, mb := range []int{512, 1024, 1973, 2048, 2560} {
		env := lowRAMGoBuildEnvForMB(mb)
		tight := false
		for _, e := range env {
			if e == "GOMEMLIMIT=900MiB" {
				tight = true
			}
		}
		if tight && !serializeGoBuildsForMB(mb) {
			t.Errorf("%dMB gets the 900MiB cap but still builds in parallel — "+
				"that is the combination that overshot the box", mb)
		}
	}
}
