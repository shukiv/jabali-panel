package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// allExist makes every slice "present on host" so the renderer emits
// every non-off user.
func allExist(string) bool  { return true }
func noneExist(string) bool { return false }

func TestRenderEgressNFT_EmitsHeaderAndDefaults(t *testing.T) {
	requireHostMutationAllowed(t)
	out := RenderEgressNFT(nil, CanonicalDefaults(), allExist)

	require.Contains(t, out, "table inet jabali_per_user {")
	require.Contains(t, out, "set default_loopback4")
	require.Contains(t, out, "127.0.0.0/8")
	require.Contains(t, out, "set default_loopback6")
	require.Contains(t, out, "::1/128")
	// GH #702: private ranges must NOT be blanket-accepted in the loopback set.
	require.NotContains(t, out, "10.0.0.0/8")
	require.NotContains(t, out, "172.16.0.0/12")
	require.NotContains(t, out, "192.168.0.0/16")
	require.NotContains(t, out, "fc00::/7")
	require.Contains(t, out, "elements = { 53, 80, 443, 587, 465, 25, 993, 995, 143, 110 }")
	require.Contains(t, out, "type cgroupsv2 : verdict")
	require.Contains(t, out, "socket cgroupv2 level 3 vmap @cgroup_to_chain")
}

func TestRenderEgressNFT_OffStateSkipped(t *testing.T) {
	requireHostMutationAllowed(t)
	users := []EgressUser{
		{Username: "alice", State: "off"},
		{Username: "bob", State: "enforced"},
	}
	out := RenderEgressNFT(users, CanonicalDefaults(), allExist)

	require.Contains(t, out, "alice: state=off — skipped")
	require.NotContains(t, out, "user_alice_drops")
	require.NotContains(t, out, "user_alice_off")
	require.Contains(t, out, "counter user_bob_drops")
	require.Contains(t, out, "chain user_bob_enforced")
	require.Contains(t, out, "counter name user_bob_drops drop")
	// GH #638: enforced drops are logged (rate-limited) so the blocked
	// daddr:dport is visible instead of a silent SYN-drop.
	require.Contains(t, out, `log prefix "jabali-egress-drop-bob `)
}

func TestRenderEgressNFT_LearningEmitsLogAndAccept(t *testing.T) {
	requireHostMutationAllowed(t)
	users := []EgressUser{{Username: "carol", State: "learning"}}
	out := RenderEgressNFT(users, CanonicalDefaults(), allExist)

	require.Contains(t, out, "chain user_carol_learning")
	require.Contains(t, out, "limit rate 5/minute log prefix \"jabali-egress-learn-carol \"")
	require.Contains(t, out, "counter name user_carol_drops accept")
	require.NotContains(t, out, "counter name user_carol_drops drop")
}

func TestRenderEgressNFT_VmapKeyMatchesM18Topology(t *testing.T) {
	requireHostMutationAllowed(t)
	users := []EgressUser{{Username: "dave", State: "enforced"}}
	out := RenderEgressNFT(users, CanonicalDefaults(), allExist)

	require.Contains(t, out,
		`"jabali.slice/jabali-user.slice/jabali-user-dave.slice" : jump user_dave_enforced`,
		"vmap key must match the actual M18 cgroup topology — see VM verification 2026-04-29")
}

func TestRenderEgressNFT_MissingSliceNoUIDSkipped(t *testing.T) {
	requireHostMutationAllowed(t)
	// No uid + missing slice — cannot enforce; skipped + surfaced as fail-open.
	users := []EgressUser{{Username: "eve", State: "enforced"}}
	out := RenderEgressNFT(users, CanonicalDefaults(), noneExist)

	require.Contains(t, out, "eve: slice")
	require.Contains(t, out, "missing + no uid — skipped")
	require.NotContains(t, out, "user_eve_drops")
}

func TestRenderEgressNFT_MissingSliceUIDFallback(t *testing.T) {
	requireHostMutationAllowed(t)
	// GH #708: missing slice BUT uid known -> enforced by uid, not fail-open.
	users := []EgressUser{{Username: "eve", State: "enforced", UID: 1001}}
	out := RenderEgressNFT(users, CanonicalDefaults(), noneExist)

	require.Contains(t, out, "counter user_eve_drops")        // chain emitted
	require.Contains(t, out, "meta skuid 1001 jump user_eve_enforced") // uid dispatch
	require.NotContains(t, out, "policy accept;\n  }")       // (chain still ends with accept, sanity below)
}

func TestRenderEgressNFT_AllowedExtraEmittedInChain(t *testing.T) {
	requireHostMutationAllowed(t)
	port := 443
	users := []EgressUser{{
		Username: "frank",
		State:    "enforced",
		AllowedExtra: []EgressExtra{
			{CIDR: "203.0.113.0/24", Port: &port, Protocol: "tcp", Comment: "monitoring"},
			{CIDR: "2001:db8::/32"},
		},
	}}
	out := RenderEgressNFT(users, CanonicalDefaults(), allExist)

	require.Contains(t, out, "ip daddr 203.0.113.0/24 tcp dport 443 accept comment \"monitoring\"")
	require.Contains(t, out, "ip6 daddr 2001:db8::/32 accept")
}

func TestRenderEgressNFT_DeterministicOrdering(t *testing.T) {
	requireHostMutationAllowed(t)
	a := []EgressUser{
		{Username: "zara", State: "enforced"},
		{Username: "alice", State: "enforced"},
	}
	b := []EgressUser{
		{Username: "alice", State: "enforced"},
		{Username: "zara", State: "enforced"},
	}
	require.Equal(t, RenderEgressNFT(a, CanonicalDefaults(), allExist),
		RenderEgressNFT(b, CanonicalDefaults(), allExist),
		"renderer must sort users so output is reproducible")
}

func TestParseNFTCounters_HappyPath(t *testing.T) {
	requireHostMutationAllowed(t)
	raw := []byte(`{
	  "nftables": [
	    {"metainfo": {"version":"1.1.3"}},
	    {"counter": {"family":"inet","table":"jabali_per_user","name":"user_alice_drops","packets":42,"bytes":1680}},
	    {"counter": {"family":"inet","table":"jabali_per_user","name":"user_bob_drops","packets":0,"bytes":0}},
	    {"counter": {"family":"inet","table":"other","name":"user_zara_drops","packets":99}}
	  ]
	}`)

	got, err := parseNFTCounters(raw)
	require.NoError(t, err)
	require.Len(t, got, 2, "counter from a different table must be filtered out")

	byName := map[string]userEgressCounter{}
	for _, c := range got {
		byName[c.Username] = c
	}
	require.Equal(t, uint64(42), byName["alice"].Packets)
	require.Equal(t, uint64(0), byName["bob"].Packets)
}

func TestExtractUsernameFromCounter_ShapeRequired(t *testing.T) {
	requireHostMutationAllowed(t)
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"user_alice_drops": {"alice", true},
		"user_z9-x_drops":  {"z9-x", true},
		"user__drops":      {"", false},
		"misc_alice_drops": {"", false},
		"user_alice_misc":  {"", false},
		"":                 {"", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := extractUsernameFromCounter(name)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestSlicePathFor_M18Format(t *testing.T) {
	requireHostMutationAllowed(t)
	require.Equal(t,
		"jabali.slice/jabali-user.slice/jabali-user-shukivaknin.slice",
		SlicePathFor("shukivaknin"))
}

func TestPortStrings(t *testing.T) {
	requireHostMutationAllowed(t)
	require.Equal(t, "53,80,443", portStrings([]int{53, 80, 443}))
	// guard against accidental empty-list panic
	require.True(t, !strings.Contains(portStrings(nil), ","))
}

// GH #401: an always-on SSRF floor drops link-local / cloud-metadata for any
// tenant slice (level 2), before the per-user vmap, regardless of enrollment.
func TestRenderEgressNFT_SSRFFloor(t *testing.T) {
	requireHostMutationAllowed(t)
	out := RenderEgressNFT(nil, CanonicalDefaults(), allExist)
	for _, want := range []string{
		"counter ssrf_floor_drops {}",
		`socket cgroupv2 level 2 "jabali.slice/jabali-user.slice" ip daddr 169.254.0.0/16 counter name ssrf_floor_drops drop`,
		`socket cgroupv2 level 2 "jabali.slice/jabali-user.slice" ip6 daddr fe80::/10 counter name ssrf_floor_drops drop`,
	} {
		if !contains(out, want) {
			t.Errorf("egress floor missing %q:\n%s", want, out)
		}
	}
	// Floor must precede the per-user dispatch so it is absolute.
	fi := indexOf(out, "169.254.0.0/16")
	vi := indexOf(out, "vmap @cgroup_to_chain")
	if fi < 0 || vi < 0 || fi > vi {
		t.Errorf("floor must come before the vmap dispatch (floor=%d vmap=%d)", fi, vi)
	}

	// When the tenant parent slice is absent, the floor (and its counter)
	// must NOT be emitted — nft would reject a cgroupv2 path that doesn't
	// exist, and an undeclared counter reference is a load error.
	noneOut := RenderEgressNFT(nil, CanonicalDefaults(), noneExist)
	if contains(noneOut, "ssrf_floor_drops") || contains(noneOut, "169.254.0.0/16") {
		t.Errorf("floor must be omitted when parent slice missing:\n%s", noneOut)
	}
}

func indexOf(s, sub string) int { return strings.Index(s, sub) }

// The rendered file must atomically replace the table (add+delete+redefine)
// so reconcile-tick reloads don't accumulate duplicate chain rules.
func TestRenderEgressNFT_AtomicReplaceHeader(t *testing.T) {
	requireHostMutationAllowed(t)
	out := RenderEgressNFT(nil, CanonicalDefaults(), allExist)
	add := indexOf(out, "add table inet jabali_per_user")
	del := indexOf(out, "delete table inet jabali_per_user")
	def := indexOf(out, "table inet jabali_per_user {")
	if add < 0 || del < 0 || def < 0 || !(add < del && del < def) {
		t.Fatalf("expected add < delete < table-definition; got add=%d del=%d def=%d\n%s", add, del, def, out)
	}
}
