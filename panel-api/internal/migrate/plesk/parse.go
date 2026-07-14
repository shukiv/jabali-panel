package plesk

import (
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// parsePleskList turns `plesk bin subscription --list` output — one
// subscription name (the primary domain) per line — into account
// summaries. Blank lines and comment/banner lines are skipped. The
// subscription name is both the source-side ID and the primary domain.
func parsePleskList(out string) []migrate.AccountSummary {
	rows := []migrate.AccountSummary{}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasPrefix(name, "#") {
			continue
		}
		rows = append(rows, migrate.AccountSummary{
			ID:     name,
			Login:  name,
			Domain: name,
		})
	}
	return rows
}
