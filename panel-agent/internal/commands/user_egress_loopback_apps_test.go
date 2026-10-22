package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	loopbackDropV4 = "meta skuid >= 1000 ip daddr 127.0.0.0/8 tcp dport @loopback_app_ports tcp flags & (syn | ack) == syn meta skuid . tcp dport != @loopback_app_owners counter name loopback_app_drops drop"
	loopbackDropV6 = "meta skuid >= 1000 ip6 daddr ::1 tcp dport @loopback_app_ports tcp flags & (syn | ack) == syn meta skuid . tcp dport != @loopback_app_owners counter name loopback_app_drops drop"
)

func loopbackTestApps() []LoopbackAppPort {
	return []LoopbackAppPort{
		{Port: 30002, UID: 1006},
		{Port: 20001, UID: 1005},
		{Port: 10003},            // admin Docker app: protected, no owner
		{Port: 20001, UID: 1005}, // duplicate
		{Port: 20009, UID: 33},   // a system uid is never an owner
		{Port: 0, UID: 1005},     // invalid port
		{Port: 70000, UID: 1005}, // invalid port
	}
}

// The allocated ports are protected; only tenant owners get a pair. Ports and
// pairs are distinct and sorted, so the file is stable and nft accepts it.
func TestRenderEgress_LoopbackAppSets(t *testing.T) {
	out := RenderEgressNFT(nil, CanonicalDefaults(), loopbackTestApps(), allExist)
	require.Contains(t, out, "  counter loopback_app_drops {}\n")
	require.Contains(t, out, "  set loopback_app_ports {\n    type inet_service\n    elements = { 10003, 20001, 20009, 30002 }\n  }\n")
	require.Contains(t, out, "  set loopback_app_owners {\n    typeof meta skuid . tcp dport\n    elements = { 1005 . 20001, 1006 . 30002 }\n  }\n")
}

// The drop runs before the per-user dispatch, whose chains end in a verdict,
// and after the SSRF floor, for IPv4 and IPv6.
func TestRenderEgress_LoopbackAppDropBeforeDispatch(t *testing.T) {
	users := []EgressUser{{Username: "alice", State: "enforced", UID: 1005}}
	out := RenderEgressNFT(users, CanonicalDefaults(), loopbackTestApps(), allExist)
	v4 := strings.Index(out, loopbackDropV4)
	v6 := strings.Index(out, loopbackDropV6)
	floor := strings.Index(out, "ip daddr 169.254.0.0/16")
	dispatch := strings.Index(out, "socket cgroupv2 level 3 vmap @cgroup_to_chain")
	require.True(t, v4 > 0 && v6 > 0, "drop rules missing:\n%s", out)
	require.Less(t, floor, v4)
	require.Less(t, v4, dispatch)
	require.Less(t, v6, dispatch)
}

// The boot file carries the same drop: it is matched by uid, needs no cgroup
// path, and the window between boot and the first tick must not reopen it.
func TestRenderEgressBoot_LoopbackAppDrop(t *testing.T) {
	out := RenderEgressBootNFT(nil, CanonicalDefaults(), loopbackTestApps())
	require.Contains(t, out, loopbackDropV4)
	require.Contains(t, out, loopbackDropV6)
	require.Contains(t, out, "elements = { 1005 . 20001, 1006 . 30002 }")
}

// No allocated port: no set, no counter and no rule. An empty
// loopback_app_ports set would also be valid nft, but there is nothing to
// protect.
func TestRenderEgress_NoLoopbackApps(t *testing.T) {
	out := RenderEgressNFT(nil, CanonicalDefaults(), nil, allExist)
	require.NotContains(t, out, "loopback_app")
}

// A port whose owner is unknown on the host stays protected, with an empty
// owner set: nft accepts a typeof set without elements.
func TestRenderEgress_LoopbackAppNoOwners(t *testing.T) {
	out := RenderEgressNFT(nil, CanonicalDefaults(), []LoopbackAppPort{{Port: 10003}}, allExist)
	require.Contains(t, out, "  set loopback_app_owners {\n    typeof meta skuid . tcp dport\n  }\n")
	require.Contains(t, out, loopbackDropV4)
}

// The payload shape panel-api sends (TestApplyUserEgress_SendsLoopbackAppPorts
// in panel-api/internal/reconciler): a port without an owner has no username.
func TestUserEgressApplyParams_DecodesLoopbackAppPorts(t *testing.T) {
	var p userEgressApplyParams
	require.NoError(t, json.Unmarshal([]byte(`{"users":[],"loopback_app_ports":[{"port":20001,"username":"alice"},{"port":10003}]}`), &p))
	require.Equal(t, []userEgressLoopbackAppPort{
		{Port: 20001, Username: "alice"},
		{Port: 10003},
	}, p.LoopbackAppPorts)
}
