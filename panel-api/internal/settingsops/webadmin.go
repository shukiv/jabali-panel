package settingsops

import (
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// StalwartWebadminPort is the HTTPS port of the panel hostname that serves
// Stalwart's WebAdmin (GH #243, ADR-0142). Distinct from the panel's :8443.
const StalwartWebadminPort = 8449

// WebadminApplyParams is the mail.webadmin.apply payload for s. The settings
// save and the reconciler both dispatch it, so the two never disagree.
func WebadminApplyParams(s *models.ServerSettings) map[string]any {
	return map[string]any{
		"enabled":       s.StalwartWebadminEnabled,
		"server_name":   s.Hostname,
		"port":          StalwartWebadminPort,
		"ssl_cert_path": "/etc/jabali/tls/panel.crt",
		"ssl_key_path":  "/etc/jabali/tls/panel.key",
		"allow_cidrs":   SplitCIDRList(s.StalwartWebadminAllowCIDRs),
	}
}

// SplitCIDRList parses a comma/space-separated allowlist into a clean slice.
func SplitCIDRList(in string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(in, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
