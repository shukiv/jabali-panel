package commands

import (
	"reflect"
	"strings"
	"testing"
)

// An SSH login is placed by logind in user.slice/user-<uid>.slice/
// session-N.scope, not in the tenant's jabali-user-<user>.slice (seen on a
// live host: a drill tenant's shell ran in session-19890.scope). The egress
// ruleset dispatched by cgroup only, so a tenant shell reached any port, and
// cloud metadata, while the same tenant's PHP could not. These tests pin the
// uid dispatch that closes that. They render only, so they run everywhere.

func sshShellUsers() []EgressUser {
	return []EgressUser{
		{Username: "alice", State: "enforced", UID: 1001},
		{Username: "carol", State: "off", UID: 1003},
	}
}

// A user whose slice exists is dispatched by uid too, after the cgroup
// dispatch.
func TestRenderEgressNFT_SliceUserIsAlsoDispatchedByUID(t *testing.T) {
	out := RenderEgressNFT(sshShellUsers(), CanonicalDefaults(), func(string) bool { return true })

	if !strings.Contains(out, "1001 : jump user_alice_enforced") {
		t.Fatalf("alice has a slice but no uid dispatch, so her SSH shell is unfiltered:\n%s", out)
	}
	cgroupAt := strings.Index(out, "socket cgroupv2 level 3 vmap @cgroup_to_chain")
	uidAt := strings.Index(out, "meta skuid vmap @uid_to_chain")
	if cgroupAt < 0 || uidAt < cgroupAt {
		t.Errorf("uid dispatch missing or ahead of the cgroup dispatch (cgroup@%d, uid@%d):\n%s", cgroupAt, uidAt, out)
	}
}

// The SSRF floor covers tenant processes outside the slice too, state=off
// included: the floor holds regardless of egress enrollment (GH #401).
func TestRenderEgressNFT_SSRFFloorCoversProcessesOutsideTheSlice(t *testing.T) {
	out := RenderEgressNFT(sshShellUsers(), CanonicalDefaults(), func(string) bool { return true })

	for _, want := range []string{
		`socket cgroupv2 level 2 "jabali.slice/jabali-user.slice" ip daddr 169.254.0.0/16`,
		"elements = { 1001, 1003 }",
		"meta skuid @tenant_uids ip daddr 169.254.0.0/16 counter name ssrf_floor_drops drop",
		"meta skuid @tenant_uids ip6 daddr fe80::/10 counter name ssrf_floor_drops drop",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	floorAt := strings.Index(out, "meta skuid @tenant_uids")
	if cgroupAt := strings.Index(out, "vmap @cgroup_to_chain"); floorAt < 0 || floorAt > cgroupAt {
		t.Errorf("the uid floor must precede every per-user dispatch:\n%s", out)
	}
	// state=off stays opt-out of the allowlist itself.
	if strings.Contains(out, "jump user_carol") {
		t.Errorf("carol is state=off and must not be dispatched:\n%s", out)
	}
}

// Two users on one uid must not produce a duplicate map key or set element:
// nft rejects the whole file for it, which drops egress enforcement for every
// tenant on the host.
func TestRenderEgressNFT_DuplicateUIDIsRenderedOnce(t *testing.T) {
	users := []EgressUser{
		{Username: "alice", State: "enforced", UID: 1001},
		{Username: "alias", State: "learning", UID: 1001},
	}
	out := RenderEgressNFT(users, CanonicalDefaults(), func(string) bool { return true })

	if n := strings.Count(out, "1001 : jump"); n != 1 {
		t.Errorf("uid 1001 has %d entries in uid_to_chain, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "elements = { 1001 }") {
		t.Errorf("tenant_uids must hold uid 1001 once:\n%s", out)
	}
	// "alias" sorts first, so it keeps the uid; alice is still reached by
	// her cgroup.
	if !strings.Contains(out, "1001 : jump user_alias_learning") {
		t.Errorf("the first user by name keeps the uid:\n%s", out)
	}
}

// A uid below minTenantUID is a system account. Matching it would put that
// daemon's traffic through a tenant chain.
func TestRenderEgressNFT_SystemUIDIsNeverMatched(t *testing.T) {
	users := []EgressUser{{Username: "odd", State: "enforced", UID: 33}}
	out := RenderEgressNFT(users, CanonicalDefaults(), func(string) bool { return true })

	if strings.Contains(out, "meta skuid") || strings.Contains(out, "33 : jump") {
		t.Errorf("uid 33 (www-data) must never be matched:\n%s", out)
	}
	if !strings.Contains(out, `"jabali.slice/jabali-user.slice/jabali-user-odd.slice" : jump user_odd_enforced`) {
		t.Errorf("the user must still be dispatched by cgroup:\n%s", out)
	}
}

// egressCoverage feeds users_fail_open in the apply response. It must count
// what the renderer does, including the duplicate and system-uid rules.
func TestEgressCoverage_MatchesTheRenderer(t *testing.T) {
	users := []EgressUser{
		{Username: "alice", State: "enforced", UID: 1001},
		{Username: "alias", State: "enforced", UID: 1001}, // duplicate uid, no slice
		{Username: "odd", State: "enforced", UID: 33},     // system uid, no slice
		{Username: "carol", State: "off", UID: 1003},
		{Username: "dave", State: "learning", UID: 1004},
		{Username: "zed", State: "enforced", UID: 1004}, // dave keeps 1004; no slice
	}
	hasSlice := func(p string) bool { return p == SlicePathFor("alice") }

	emitted, skipped, failOpen := egressCoverage(users, hasSlice)
	out := RenderEgressNFT(users, CanonicalDefaults(), hasSlice)

	// alias sorts first and keeps uid 1001, so alice (who has a slice) and
	// dave (uid 1004) are emitted with it. odd has neither a slice nor a
	// tenant uid, and zed's uid is already dave's: both fail open.
	if emitted != 3 || skipped != 3 || !reflect.DeepEqual(failOpen, []string{"odd", "zed"}) {
		t.Errorf("coverage = emitted %d skipped %d failOpen %v, want 3, 3, [odd zed]", emitted, skipped, failOpen)
	}
	for _, u := range []string{"alias", "alice", "dave"} {
		if !strings.Contains(out, "counter user_"+u+"_drops {}") {
			t.Errorf("coverage counts %s as emitted but the renderer skipped it:\n%s", u, out)
		}
	}
	for _, u := range []string{"odd", "zed"} {
		if strings.Contains(out, "counter user_"+u+"_drops") {
			t.Errorf("coverage reports %s as fail-open but the renderer emitted it:\n%s", u, out)
		}
	}
}
