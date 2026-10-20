// Package dbshadow names the per-account MariaDB shadow-admin user the
// phpMyAdmin SSO bridge signs in as (<panel username>_mysqladmin).
//
// The agent creates one only for a panel username that PanelUsername
// accepts (db.mysqladmin.ensure), and the panel's user delete has to know
// which names can exist. Both read the rule from here (single go module, same
// convention as internal/dbreserve), so the two can't drift.
package dbshadow

import "regexp"

// PanelUsername is the panel usernames a shadow-admin is made for: a lowercase
// letter first, then lowercase letters, digits, underscores and hyphens, 1–32
// chars so the "_mysqladmin" suffix stays inside the agent's 64-char database
// user rule. A panel username may also start with "_"; that one gets no
// shadow, as the agent's database user names start with a letter.
var PanelUsername = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// MysqladminSuffix ends every MariaDB shadow-admin user name.
const MysqladminSuffix = "_mysqladmin"

// MysqladminUser returns the shadow-admin user name for panelUsername, and
// false when the agent never makes one for that username.
func MysqladminUser(panelUsername string) (string, bool) {
	if !PanelUsername.MatchString(panelUsername) {
		return "", false
	}
	return panelUsername + MysqladminSuffix, true
}
