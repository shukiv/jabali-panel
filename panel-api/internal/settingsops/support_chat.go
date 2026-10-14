package settingsops

import (
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// supportChatCSPTimeout bounds nginx.panel_csp.apply: a file write, nginx -t
// and a reload, like the nginx tunables.
const supportChatCSPTimeout = 60 * time.Second

// SupportChatCSPParams is the exact nginx.panel_csp.apply wire: whether the
// panel's CSP allows the support chat's origin. It does while the chat is on
// in either panel.
func SupportChatCSPParams(s *models.ServerSettings) map[string]any {
	return map[string]any{"support_chat": s.SupportChatEnabled()}
}

// SupportChatEffect is the CSP side effect of Server Settings -> Support chat.
// touched is whether the request carried either chat switch; a re-save
// re-applies (Reapply), so saving the card heals a drifted snippet.
//
// A failed apply that was turning the chat ON reverts both switches: the
// widget can't load under the old CSP, so the DB must not claim it is on. A
// failed apply turning it OFF keeps the switches off: the SPA stops loading the
// widget at once, and a CSP that still allows the origin is only wider than
// needed until the next panel start re-applies it.
func SupportChatEffect(before, after *models.ServerSettings, touched bool) Effect {
	e := effect(touched, "nginx.panel_csp.apply",
		SupportChatCSPParams(before), SupportChatCSPParams(after), supportChatCSPTimeout)
	e.RevertOnFailure = e.Kind == Changed && after.SupportChatEnabled()
	return e
}
