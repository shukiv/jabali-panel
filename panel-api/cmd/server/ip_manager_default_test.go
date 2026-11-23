package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/settingsops"
)

// `jabali ip update <id> --default` on an IPv4 moves the host files pinned to
// the old address with the same call as the REST PATCH; an IPv6 default does not, and a
// failed call is reported after the DB change.
func TestFollowDefaultIPv4(t *testing.T) {
	saved := sharedAgent
	sharedAgent = agent.NewClient(agent.Config{SocketPath: "/nonexistent"})
	t.Cleanup(func() { sharedAgent = saved })

	calls := stubSettingsDispatch(t, nil)
	require.NoError(t, followDefaultIPv4(context.Background(), &models.ManagedIP{ID: 2, Address: "203.0.113.9", Family: "ipv4"}))
	require.NoError(t, followDefaultIPv4(context.Background(), &models.ManagedIP{ID: 3, Address: "2001:db8::9", Family: "ipv6"}))
	require.Equal(t, []settingsops.AgentCall{{
		Method:  "system.default_ipv4_rehost",
		Params:  map[string]any{"ipv4": "203.0.113.9"},
		Timeout: 30 * time.Second,
	}}, *calls)

	stubSettingsDispatch(t, errors.New("203.0.113.9 is not on any network interface"))
	err := followDefaultIPv4(context.Background(), &models.ManagedIP{ID: 2, Address: "203.0.113.9", Family: "ipv4"})
	require.ErrorContains(t, err, "IP 2 (203.0.113.9) is the default now")
	require.ErrorContains(t, err, "not on any network interface")
}
