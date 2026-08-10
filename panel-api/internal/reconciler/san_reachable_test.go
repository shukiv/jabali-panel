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

// webNameKeepsFrontedAllowance: apex and www keep the CDN allowance; the mail
// helpers do not (they are served from a different webroot and their own cert,
// so a proxied helper's challenge 404s and would tank the whole web cert).
func TestWebNameKeepsFrontedAllowance(t *testing.T) {
	const apex = "arizot-e.com"
	for _, tc := range []struct {
		name string
		want bool
	}{
		{apex, true},
		{"www." + apex, true},
		{"mail." + apex, false},
		{"autoconfig." + apex, false},
		{"autodiscover." + apex, false},
		{"mta-sts." + apex, false},
	} {
		if got := webNameKeepsFrontedAllowance(tc.name, apex); got != tc.want {
			t.Errorf("webNameKeepsFrontedAllowance(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The arizot-e.com incident, expressed through the two reachability calls
// reachableSANs makes per name. Behind Cloudflare the helper and the apex both
// resolve to CF; www rides the fronted allowance (kept) while mail. is forced
// to the direct test (dropped) so it cannot fail the whole cert.
func TestHelperVsWebSANDecision(t *testing.T) {
	const (
		ourIP = "203.0.113.10"
		cf1   = "104.16.1.1"
		cf2   = "104.16.2.2"
	)
	apexAddrs := []string{cf1, cf2}
	ours := []string{ourIP}
	proxied := []string{cf1} // helper + www both sit behind the same CDN

	// www.<apex>: fronted allowance applies -> kept.
	if !sanReachability(proxied, apexAddrs, ours) {
		t.Error("www behind the apex CDN was dropped — proxied web names must be kept")
	}
	// mail.<apex>: helper, direct-only (nil apex) -> dropped, since it resolves
	// to CF, not to us. This is the fix: the proxied helper no longer rides the
	// apex match, so it cannot 404 and kill the apex's cert.
	if sanReachability(proxied, nil, ours) {
		t.Error("proxied mail helper was kept — it must be dropped (direct-only)")
	}
	// Helper that DOES point straight at us stays on the web cert.
	if !sanReachability([]string{ourIP}, nil, ours) {
		t.Error("helper resolving directly to us was dropped")
	}
	// Fail-open preserved: unknown ourAddrs -> keep the helper rather than guess.
	if !sanReachability([]string{cf1}, nil, nil) {
		t.Error("helper dropped when we cannot judge — filter must never invent a failure")
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
