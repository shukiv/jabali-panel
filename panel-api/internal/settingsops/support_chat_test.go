package settingsops

import (
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func chat(admin, clients bool) *models.ServerSettings {
	return &models.ServerSettings{ID: 1, SupportChatAdmin: admin, SupportChatClients: clients}
}

// The panel CSP allows the support chat's origin while the chat is on in
// either panel, so the wire is the OR of the two switches.
func TestSupportChatEffect(t *testing.T) {
	cases := []struct {
		name          string
		before, after *models.ServerSettings
		touched       bool
		kind          Kind
		wire          bool
		revert        bool
	}{
		{"untouched sends nothing", chat(false, false), chat(false, false), false, NoOp, false, false},
		{"admin on", chat(false, false), chat(true, false), true, Changed, true, true},
		{"clients on", chat(false, false), chat(false, true), true, Changed, true, true},
		{"last one off", chat(true, false), chat(false, false), true, Changed, false, false},
		{"one of two off keeps the CSP", chat(true, true), chat(true, false), true, Reapply, true, false},
		{"second one on keeps the CSP", chat(true, false), chat(true, true), true, Reapply, true, false},
		{"re-save re-applies", chat(false, false), chat(false, false), true, Reapply, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := SupportChatEffect(tc.before, tc.after, tc.touched)
			require.Equal(t, tc.kind, e.Kind)
			require.Equal(t, tc.revert, e.RevertOnFailure, "only a failed enable reverts")
			if tc.kind == NoOp {
				require.Nil(t, e.Call)
				return
			}
			require.NotNil(t, e.Call)
			require.Equal(t, "nginx.panel_csp.apply", e.Call.Method)
			require.Equal(t, map[string]any{"support_chat": tc.wire}, e.Call.Params)
			require.Positive(t, e.Call.Timeout)
		})
	}
}
