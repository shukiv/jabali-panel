package reconciler

import "testing"

// JAB-226. The pre-existing filter asks "does this name resolve". That is not
// the question — a name can resolve perfectly and still point at somebody else's
// server, which is the normal state of a migration until the source nameservers
// stop being authoritative. The challenge then lands on the OLD box, 404s, and
// one failed name fails the whole certificate including the apex.
func TestSANReachability(t *testing.T) {
	const (
		ourIP    = "203.0.113.10"
		oldBoxIP = "198.51.100.20"
		cfIP1    = "104.16.1.1"
		cfIP2    = "104.16.2.2"
	)

	for _, tc := range []struct {
		name     string
		san      []string
		apex     []string
		ours     []string
		wantKeep bool
		why      string
	}{
		{
			name: "points at us directly", san: []string{ourIP},
			apex: []string{ourIP}, ours: []string{ourIP},
			wantKeep: true, why: "the ordinary case",
		},
		{
			name: "still points at the source box", san: []string{oldBoxIP},
			apex: []string{ourIP}, ours: []string{ourIP},
			wantKeep: false, why: "THE BUG: resolves fine, challenge lands elsewhere, kills the whole cert",
		},
		{
			name: "behind a proxy, same as apex", san: []string{cfIP1, cfIP2},
			apex: []string{cfIP1, cfIP2}, ours: []string{ourIP},
			wantKeep: true, why: "nothing resolves to us when proxied, but the proxy forwards the challenge",
		},
		{
			name: "proxied apex, SAN left on the old box", san: []string{oldBoxIP},
			apex: []string{cfIP1}, ours: []string{ourIP},
			wantKeep: false, why: "exactly the newhighsite incident",
		},
		{
			name: "no DNS at all", san: nil,
			apex: []string{ourIP}, ours: []string{ourIP},
			wantKeep: false, why: "pre-existing resolvableSANs rule preserved",
		},
		{
			name: "cannot judge — keep", san: []string{oldBoxIP},
			apex: nil, ours: nil,
			wantKeep: true, why: "this filter narrows; it must never invent a reason to fail",
		},
		{
			name: "partial overlap with apex counts", san: []string{cfIP2},
			apex: []string{cfIP1, cfIP2}, ours: []string{ourIP},
			wantKeep: true, why: "round-robin A records legitimately return subsets",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanReachability(tc.san, tc.apex, tc.ours); got != tc.wantKeep {
				t.Errorf("keep = %v, want %v — %s", got, tc.wantKeep, tc.why)
			}
		})
	}
}

// IPv6-only hosts must not be judged against an empty v4 list.
func TestSANReachability_IPv6(t *testing.T) {
	v6 := "2001:db8::1"
	if !sanReachability([]string{v6}, nil, []string{v6}) {
		t.Error("a SAN matching our IPv6 was dropped")
	}
}

func TestIntersects(t *testing.T) {
	if intersects(nil, []string{"a"}) || intersects([]string{"a"}, nil) {
		t.Error("empty input must not intersect")
	}
	if !intersects([]string{"a", "b"}, []string{"b", "c"}) {
		t.Error("shared element not detected")
	}
	if intersects([]string{"a"}, []string{"b"}) {
		t.Error("disjoint sets reported as intersecting")
	}
}
