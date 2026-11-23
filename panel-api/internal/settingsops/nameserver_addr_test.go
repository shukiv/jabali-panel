package settingsops

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestNameserverAddrCall(t *testing.T) {
	old := &models.ServerSettings{NS1IPv4: "203.0.113.121", NS2IPv4: "203.0.113.121"}

	require.Nil(t, NameserverAddrCall(old, &models.ServerSettings{NS1IPv4: "203.0.113.121", NS2IPv4: "203.0.113.121"}), "no change")
	require.Nil(t, NameserverAddrCall(old, &models.ServerSettings{}), "cleared")

	call := NameserverAddrCall(old, &models.ServerSettings{NS1IPv4: "203.0.113.9", NS2IPv4: "203.0.113.9"})
	require.NotNil(t, call)
	require.Equal(t, "dns.pdns_rebind", call.Method)
	require.Equal(t, []string{"203.0.113.9"}, call.Params["addresses"], "one address when ns1 and ns2 share it")

	call = NameserverAddrCall(old, &models.ServerSettings{NS1IPv4: "203.0.113.121", NS2IPv4: "203.0.113.10"})
	require.Equal(t, []string{"203.0.113.121", "203.0.113.10"}, call.Params["addresses"])
}

// The params are the agent's dns.pdns_rebind request.
func TestNameserverAddrCall_MatchesTheAgentContract(t *testing.T) {
	call := NameserverAddrCall(&models.ServerSettings{}, &models.ServerSettings{NS1IPv4: "203.0.113.9"})
	got, err := json.Marshal(call.Params)
	require.NoError(t, err)
	want, err := os.ReadFile("../agent/testdata/dns_pdns_rebind_request.json")
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
}
