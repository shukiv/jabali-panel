package notifications

// maxPanelPathLen bounds a notification link. The longest one any producer
// builds is well under 200 bytes.
const maxPanelPathLen = 2048

// PanelPath returns link when it is a path inside the panel, such as
// "/jabali-admin/backups" or "/admin/security?tab=malware", and "" otherwise.
//
// Notification links are in-panel by contract: every producer passes a
// constant path or an admin-entered one. The web-push worker navigates to the
// link and the email body puts it in an href, so a full URL, a "//host" link,
// a backslash, a control character or a quote is dropped rather than carried
// to the bell, the push worker or a channel.
func PanelPath(link string) string {
	if link == "" || len(link) > maxPanelPathLen || link[0] != '/' {
		return ""
	}
	if len(link) > 1 && link[1] == '/' {
		return ""
	}
	for i := 0; i < len(link); i++ {
		if !panelPathByte(link[i]) {
			return ""
		}
	}
	return link
}

// panelPathByte allows letters, digits and the URL punctuation a path, query
// or fragment needs. It leaves out space, quotes, angle brackets, backslash,
// "|", backtick, braces, control bytes and anything non-ASCII.
func panelPathByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '/', '-', '.', '_', '~', '?', '&', '=', '%', '+', ':', '@', '#', '!', '$', '(', ')', '*', ',', ';':
		return true
	}
	return false
}
