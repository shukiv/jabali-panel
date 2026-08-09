package commands

import (
	"strings"
	"testing"
)

func bootTestUsers() []EgressUser {
	return []EgressUser{
		{Username: "alice", State: "enforced", UID: 1001},
		{Username: "bob", State: "learning", UID: 1002},
		{Username: "carol", State: "off", UID: 1003},
	}
}

// TestRenderEgressBootNFT_HasNoCgroupPaths is the whole point of the boot
// variant, so it is asserted directly rather than inferred from behaviour.
//
// nftables resolves a cgroupv2 path to an inode when the rule loads. At boot no
// tenant process has started, so jabali.slice/jabali-user.slice does not exist,
// and because `nft -f` applies a file as a single transaction ONE unresolvable
// path discards every rule in it. Confirmed against a live host: loading the
// cgroup ruleset with no slices present left the table not merely incomplete
// but absent —
//
//	Error: cgroupv2 path fails: No such file or directory
//	$ nft list table inet jabali_per_user  ->  No such file or directory
//
// while the boot variant loaded and left six enforcing rules in place.
func TestRenderEgressBootNFT_HasNoCgroupPaths(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	out := RenderEgressBootNFT(bootTestUsers(), CanonicalDefaults())

	// The slice prefix is what nft has to resolve. Its absence is the
	// invariant; an empty `type cgroupsv2 : verdict` map declaration is fine
	// because a type is not a path.
	if strings.Contains(out, "jabali.slice/") {
		t.Errorf("boot ruleset references a cgroup path — it will be rejected "+
			"wholesale at boot when the slice does not exist yet:\n%s", out)
	}
	if strings.Contains(out, "socket cgroupv2 level 2") {
		t.Errorf("boot ruleset uses a level-2 cgroup match, which names the "+
			"tenant parent slice:\n%s", out)
	}
}

// TestRenderEgressBootNFT_EnforcesEveryNonOffUserByUID checks the boot file
// still does its job. Fail-closed matters more here than anywhere else: this
// is the ruleset covering the window where nothing else is running.
func TestRenderEgressBootNFT_EnforcesEveryNonOffUserByUID(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	out := RenderEgressBootNFT(bootTestUsers(), CanonicalDefaults())

	for _, want := range []string{
		"meta skuid 1001 jump user_alice_enforced",
		"meta skuid 1002 jump user_bob_learning",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q — that user is unprotected until the first "+
				"reconciler tick:\n%s", want, out)
		}
	}
	// state=off is opt-out, and must stay opt-out at boot too.
	if strings.Contains(out, "user_carol") && strings.Contains(out, "jump user_carol") {
		t.Errorf("carol is state=off and must not be dispatched:\n%s", out)
	}
}

// TestRenderEgressBootNFT_KeepsSSRFFloor guards the GH #401 floor across the
// boot window. 169.254.169.254 is the cloud metadata endpoint, so losing this
// for the first minute of every boot is the most valuable minute to lose it.
//
// The floor is emitted per-UID here rather than scoped to the tenant parent
// slice, which covers every user with an egress policy row rather than every
// tenant process. Narrower than the steady-state floor; wider than the nothing
// that loaded before.
func TestRenderEgressBootNFT_KeepsSSRFFloor(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	out := RenderEgressBootNFT(bootTestUsers(), CanonicalDefaults())

	for _, want := range []string{
		"meta skuid 1001 ip daddr 169.254.0.0/16 counter name ssrf_floor_drops drop",
		"meta skuid 1001 ip6 daddr fe80::/10 counter name ssrf_floor_drops drop",
		"meta skuid 1002 ip daddr 169.254.0.0/16 counter name ssrf_floor_drops drop",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing SSRF floor rule %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "counter ssrf_floor_drops {}") {
		t.Error("floor rules reference ssrf_floor_drops but the counter is not declared")
	}

	// The floor must precede the per-user dispatch, or a user chain's `accept`
	// can return before the floor is ever evaluated.
	floorAt := strings.Index(out, "169.254.0.0/16")
	jumpAt := strings.Index(out, "jump user_alice_enforced")
	if floorAt < 0 || jumpAt < 0 || floorAt > jumpAt {
		t.Errorf("SSRF floor must come before per-user dispatch (floor@%d, jump@%d):\n%s",
			floorAt, jumpAt, out)
	}
}

// TestRenderEgressBootNFT_NoUsersIsStillValid covers a fresh host: the file has
// to be loadable even with nothing to enforce, because the boot unit runs
// unconditionally and a parse error there is a failed unit on every boot.
func TestRenderEgressBootNFT_NoUsersIsStillValid(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	out := RenderEgressBootNFT(nil, CanonicalDefaults())

	if strings.Contains(out, "jabali.slice/") {
		t.Errorf("empty boot ruleset still references a cgroup path:\n%s", out)
	}
	if !strings.Contains(out, "table inet jabali_per_user {") {
		t.Errorf("boot ruleset must still declare the table:\n%s", out)
	}
	// With no users there is nothing to drop, so the counter must not be
	// declared either — a declared-but-unreferenced counter reads as a broken
	// floor when someone checks it and finds it permanently zero.
	if strings.Contains(out, "counter ssrf_floor_drops") {
		t.Errorf("no users means no floor rules, so no floor counter:\n%s", out)
	}
}

// TestRenderEgressBootNFT_SkipsUsersWithoutUID covers the one case the boot
// variant genuinely cannot handle. Without a cgroup slice AND without a uid
// there is no way to match the user's traffic, and inventing one would be
// worse than omitting them.
func TestRenderEgressBootNFT_SkipsUsersWithoutUID(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	out := RenderEgressBootNFT([]EgressUser{
		{Username: "nouid", State: "enforced", UID: 0},
		{Username: "good", State: "enforced", UID: 1005},
	}, CanonicalDefaults())

	if strings.Contains(out, "jump user_nouid") {
		t.Errorf("user with no uid cannot be dispatched at boot:\n%s", out)
	}
	if strings.Contains(out, "meta skuid 0") {
		t.Errorf("uid 0 is root, never a tenant — must not be matched:\n%s", out)
	}
	if !strings.Contains(out, "meta skuid 1005 jump user_good_enforced") {
		t.Errorf("a user with a valid uid must still be enforced:\n%s", out)
	}
}

// TestRenderEgressNFT_CgroupVariantUnchanged pins that adding the boot variant
// did not alter the steady-state ruleset, which is the one actually enforcing
// for almost all of a host's uptime.
func TestRenderEgressNFT_CgroupVariantUnchanged(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	out := RenderEgressNFT(bootTestUsers(), CanonicalDefaults(), func(string) bool { return true })

	for _, want := range []string{
		`"jabali.slice/jabali-user.slice/jabali-user-alice.slice" : jump user_alice_enforced`,
		`socket cgroupv2 level 2 "jabali.slice/jabali-user.slice" ip daddr 169.254.0.0/16`,
		"socket cgroupv2 level 3 vmap @cgroup_to_chain",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cgroup ruleset lost %q:\n%s", want, out)
		}
	}
	// When slices exist, dispatch is by cgroup — the uid fallback is for
	// missing slices only.
	if strings.Contains(out, "meta skuid 1001 jump") {
		t.Errorf("uid fallback leaked into the cgroup ruleset:\n%s", out)
	}
}
