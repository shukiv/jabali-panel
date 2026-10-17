package settingsops

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestHostnameEffects(t *testing.T) {
	before := &models.ServerSettings{Hostname: "old.example.com", PublicIPv4: "203.0.113.5"}

	t.Run("no change dispatches nothing", func(t *testing.T) {
		same := *before
		require.Empty(t, HostnameEffects(before, &same).Calls())
	})

	t.Run("a cleared hostname dispatches nothing", func(t *testing.T) {
		after := *before
		after.Hostname = ""
		require.Empty(t, HostnameEffects(before, &after).Calls())
	})

	t.Run("a rename sets the OS name with the public IP and re-points the landing vhost", func(t *testing.T) {
		after := *before
		after.Hostname = "new.example.com"
		after.PublicIPv4 = "203.0.113.9"
		require.Equal(t, []AgentCall{
			{
				Method:  "system.set_hostname",
				Params:  map[string]any{"hostname": "new.example.com", "public_ipv4": "203.0.113.9"},
				Timeout: 30 * time.Second,
			},
			{
				Method:  "nginx.panel_landing_rehost",
				Params:  map[string]any{"hostname": "new.example.com"},
				Timeout: 30 * time.Second,
			},
		}, HostnameEffects(before, &after).Calls())
	})
}
