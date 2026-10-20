// Package useragents holds the rules for the admin's server-wide list of
// blocked user agents: panel-api checks the setting with them, and the agent
// checks again before it writes the list into nginx config.
//
// An entry is matched as a case-insensitive substring of the User-Agent
// header. The allowed characters are the ones user-agent product tokens use;
// none of them can end a quoted nginx string or a regex group, so an entry
// cannot change the config around it.
package useragents

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxBlocked is the most entries the list may hold.
const MaxBlocked = 200

// essentialClients are user agents no entry may match. A blocked entry is
// refused before any location, /.well-known/acme-challenge/ included, so an
// entry such as "compatible" would stop certificate renewals, and one such
// as "Mozilla" or "bot" every visitor or search engine on every site.
var essentialClients = []struct{ name, ua string }{
	{"Chrome on Windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
	{"Chrome on Android", "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36"},
	{"Safari on macOS", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"},
	{"Safari on iPhone", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"},
	{"Firefox", "Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0"},
	{"Let's Encrypt", "Mozilla/5.0 (compatible; Let's Encrypt validation server; +https://www.letsencrypt.org)"},
	{"Googlebot", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"},
	{"Bingbot", "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)"},
}

var blockedRe = regexp.MustCompile(`^[A-Za-z0-9 ._/+-]{3,128}$`)

// Valid reports whether s may be an entry: 3 to 128 letters, digits, spaces
// and . _ / + -.
func Valid(s string) bool { return blockedRe.MatchString(s) }

// Normalize trims each entry, drops empty ones and case-insensitive
// duplicates (keeping the first spelling), and checks what is left: each
// must be Valid and must not match an essential client.
func Normalize(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		if !Valid(e) {
			return nil, fmt.Errorf("%q: an entry is 3-128 letters, digits, spaces and . _ / + -", e)
		}
		k := strings.ToLower(e)
		for _, c := range essentialClients {
			if strings.Contains(strings.ToLower(c.ua), k) {
				return nil, fmt.Errorf("%q would also block %s", e, c.name)
			}
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	if len(out) > MaxBlocked {
		return nil, fmt.Errorf("%d entries; at most %d", len(out), MaxBlocked)
	}
	return out, nil
}
