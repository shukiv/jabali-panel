package settingsops

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The params are the agent's nginx.default_vhost_listen request; the agent
// decodes the same fixture with unknown fields refused.
func TestDefaultIPv4ListenCall_MatchesTheAgentContract(t *testing.T) {
	call := DefaultIPv4ListenCall("203.0.113.9")
	require.Equal(t, "nginx.default_vhost_listen", call.Method)
	got, err := json.Marshal(call.Params)
	require.NoError(t, err)
	want, err := os.ReadFile("../agent/testdata/nginx_default_vhost_listen_request.json")
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
}
