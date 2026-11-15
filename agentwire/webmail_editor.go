package agentwire

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Webmail document editor (GH #2501). Bulwark opens office files from its
// Files view in any WOPI editor (EuroOffice, ONLYOFFICE, Collabora) when
// WOPI_CLIENT_URL names one. panel-api's reconciler sends
// webmail.editor.apply the server-wide URL; the agent writes it to webmail's
// environment. Both sides use the rule below, so they cannot drift apart.

// WebmailEditorMaxURL is the most characters of the editor's address, the
// width of the server_settings column that holds it.
const WebmailEditorMaxURL = 255

// ValidWebmailEditorURL: empty (editing off), or an absolute https URL with
// a host, no user info, no query and no fragment, at most
// WebmailEditorMaxURL characters, no control characters or whitespace. A
// path is allowed: Bulwark treats a URL with a path as the discovery URL
// itself, and a bare origin as <origin>/hosting/discovery. Plain http is
// refused: the editor opens in a frame inside the https webmail.
func ValidWebmailEditorURL(s string) bool {
	if s == "" {
		return true
	}
	if utf8.RuneCountInString(s) > WebmailEditorMaxURL ||
		strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 ||
		strings.ContainsAny(s, "?#") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
