package settingsops

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The params are the agent's system.default_ipv4_rehost request; the agent
// decodes the same fixture with unknown fields refused.
func TestDefaultIPv4RehostCall_MatchesTheAgentContract(t *testing.T) {
	call := DefaultIPv4RehostCall("203.0.113.9")
	require.Equal(t, "system.default_ipv4_rehost", call.Method)
	got, err := json.Marshal(call.Params)
	require.NoError(t, err)
	want, err := os.ReadFile("../agent/testdata/system_default_ipv4_rehost_request.json")
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
}
