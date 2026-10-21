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

// essentialClients are user agents no entry may match: the browsers, Let's
// Encrypt, and every crawler and fetcher Google and Microsoft Bing publish
// (developers.google.com/search/docs/crawling-indexing, the common,
// special-case and user-triggered lists; bing.com/webmasters "Which crawlers
// does Bing use"). A blocked entry is refused before any location,
// /.well-known/acme-challenge/ included, so an entry such as "compatible"
// would stop certificate renewals, and one such as "Mozilla" or "bot" every
// visitor or search engine on every site. Google and Bing are never blocked.
// Chrome/120.0.0.0 stands in for the W.X.Y.Z version the lists leave open.
var essentialClients = []struct{ name, ua string }{
	{"Chrome on Windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
	{"Chrome on Android", "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36"},
	{"Safari on macOS", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"},
	{"Safari on iPhone", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"},
	{"Firefox", "Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0"},
	{"Let's Encrypt", "Mozilla/5.0 (compatible; Let's Encrypt validation server; +https://www.letsencrypt.org)"},

	{"Googlebot", "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"},
	{"Googlebot", "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Googlebot/2.1; +http://www.google.com/bot.html) Chrome/120.0.0.0 Safari/537.36"},
	{"Googlebot", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"},
	{"Googlebot", "Googlebot/2.1 (+http://www.google.com/bot.html)"},
	{"Googlebot-Image", "Googlebot-Image/1.0"},
	{"Googlebot-Video", "Googlebot-Video/1.0"},
	{"Google StoreBot", "Mozilla/5.0 (X11; Linux x86_64; Storebot-Google/1.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
	{"Google StoreBot", "Mozilla/5.0 (Linux; Android 8.0; Pixel 2 Build/OPD3.170816.012; Storebot-Google/1.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36"},
	{"Google-InspectionTool", "Mozilla/5.0 (compatible; Google-InspectionTool/1.0)"},
	{"Google-InspectionTool", "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36 (compatible; Google-InspectionTool/1.0)"},
	{"GoogleOther", "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36 (compatible; GoogleOther)"},
	{"GoogleOther", "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GoogleOther) Chrome/120.0.0.0 Safari/537.36"},
	{"GoogleOther-Image", "GoogleOther-Image/1.0"},
	{"GoogleOther-Video", "GoogleOther-Video/1.0"},
	{"Google-CloudVertexBot", "Google-CloudVertexBot"},
	{"APIs-Google", "APIs-Google (+https://developers.google.com/webmasters/APIs-Google.html)"},
	{"AdsBot-Google", "AdsBot-Google (+http://www.google.com/adsbot.html)"},
	{"AdsBot-Google-Mobile", "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36 (compatible; AdsBot-Google-Mobile; +http://www.google.com/mobile/adsbot.html)"},
	{"Google-Safety", "Google-Safety"},
	{"Mediapartners-Google", "Mediapartners-Google"},
	{"Mediapartners-Google", "Mozilla/5.0 (Linux; Android 14; Pixel 8) (compatible; Mediapartners-Google/2.1; +http://www.google.com/bot.html)"},
	{"Google Chrome Web Store", "Mozilla/5.0 (compatible; Google-CWS)"},
	{"Google Feedfetcher", "FeedFetcher-Google; (+http://www.google.com/feedfetcher.html)"},
	{"Google Gemini Notebook", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36 (compatible; Google-GeminiNotebook; +https://developers.google.com/crawling/docs/crawlers-fetchers/google-gemininotebook)"},
	{"Google NotebookLM", "Google-NotebookLM"},
	{"Google-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko; compatible; Google-Agent; +https://developers.google.com/crawling/docs/crawlers-fetchers/google-agent) Chrome/120.0.0.0 Safari/537.36"},
	{"Google Messages", "GoogleMessages"},
	{"Google Pinpoint", "Google-Pinpoint"},
	{"Google Publisher Center", "GoogleProducer; (+https://developers.google.com/search/docs/crawling-indexing/google-producer)"},
	{"Google Read Aloud", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36 (compatible; Google-Read-Aloud; +https://support.google.com/webmasters/answer/1061943)"},
	{"Google Read Aloud", "google-speakr"},
	{"Google Site Verifier", "Mozilla/5.0 (compatible; Google-Site-Verification/1.0)"},

	{"Bingbot", "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm) Chrome/120.0.0.0 Safari/537.36"},
	{"Bingbot", "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)"},
	{"Bingbot", "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)"},
	{"Bing AdIdxBot", "Mozilla/5.0 (compatible; adidxbot/2.0; +http://www.bing.com/bingbot.htm)"},
	{"Bing AdIdxBot", "Mozilla/5.0 (iPhone; CPU iPhone OS 7_0 like Mac OS X) AppleWebKit/537.51.1 (KHTML, like Gecko) Version/7.0 Mobile/11A465 Safari/9537.53 (compatible; adidxbot/2.0; +http://www.bing.com/bingbot.htm)"},
	{"Bing AdIdxBot", "Mozilla/5.0 (Windows Phone 8.1; ARM; Trident/7.0; Touch; rv:11.0; IEMobile/11.0; NOKIA; Lumia 530) like Gecko (compatible; adidxbot/2.0; +http://www.bing.com/bingbot.htm)"},
	{"MicrosoftPreview", "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; MicrosoftPreview/2.0; +https://aka.ms/MicrosoftPreview) Chrome/120.0.0.0 Safari/537.36"},
	{"MicrosoftPreview", "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36 (compatible; MicrosoftPreview/2.0; +https://aka.ms/MicrosoftPreview)"},
	{"BingVideoPreview", "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; BingVideoPreview/1.0; +https://aka.ms/microsoftbots) Chrome/120.0.0.0 Safari/537.36"},
	{"BingVideoPreview", "Mozilla/5.0 (compatible; BingVideoPreview/1.0; +https://aka.ms/microsoftbots) 120.0.0.0 Safari/537.36"},
	{"BingVideoPreview", "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36 (compatible; BingVideoPreview/1.0; +https://aka.ms/microsoftbots)"},
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
		if name := Protected(e); name != "" {
			return nil, fmt.Errorf("%q would also block %s", e, name)
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

// protectedNames are words that name a Google or Bing crawler. An entry that
// contains one is refused even when no listed user agent contains the entry
// (Googlebot-News, BingPreview, msnbot), so a crawler added or renamed later
// is still never blocked.
var protectedNames = []struct{ word, name string }{
	{"google", "Google's crawlers"},
	{"bing", "Bing's crawlers"},
	{"msnbot", "Bing's crawlers"},
	{"adidxbot", "Bing's crawlers"},
	{"microsoftpreview", "Bing's crawlers"},
}

// Protected returns the name of the browser, Let's Encrypt or Google or Bing
// crawler that s would also match (case-insensitive), or "" when it matches
// none of them.
func Protected(s string) string {
	k := strings.ToLower(s)
	for _, c := range essentialClients {
		if strings.Contains(strings.ToLower(c.ua), k) {
			return c.name
		}
	}
	for _, n := range protectedNames {
		if strings.Contains(k, n.word) {
			return n.name
		}
	}
	return ""
}
