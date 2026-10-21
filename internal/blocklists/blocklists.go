// Package blocklists is the catalog of public IP blocklists the admin can
// add to CrowdSec on top of its community blocklist, and the rules for the
// admin's own (custom) list URLs. panel-api stores and shows the choice; the
// agent fetches each catalog list from the URL here and each custom list from
// a URL NormalizeCustom accepts, drops what must not be banned, and imports
// the rest as CrowdSec ban decisions.
package blocklists

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// List is one blocklist in the catalog.
type List struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"-"`
}

// Catalog is every list the admin can turn on, in the order they are shown.
// FireHOL's three levels go from few false positives (level 1) to more
// coverage and more false positives (level 3); a higher level does not
// include the lower ones.
var Catalog = []List{
	{
		Key:         "firehol_level1",
		Name:        "FireHOL level 1",
		Description: "Networks that should never reach a server: Spamhaus DROP hijacked networks, DShield's top attacking networks and Feodo botnet controllers. Very few false positives.",
		URL:         "https://iplists.firehol.org/files/firehol_level1.netset",
	},
	{
		Key:         "firehol_level2",
		Name:        "FireHOL level 2",
		Description: "Addresses seen attacking in about the last 48 hours (blocklist.de, DShield, GreenSnow). Some false positives.",
		URL:         "https://iplists.firehol.org/files/firehol_level2.netset",
	},
	{
		Key:         "firehol_level3",
		Name:        "FireHOL level 3",
		Description: "Addresses reported for attacks, spyware or viruses in the last 30 days (BruteForceBlocker, CI Army, DShield, VXVault and others). Catches more, with more false positives.",
		URL:         "https://iplists.firehol.org/files/firehol_level3.netset",
	},
}

// ReasonPrefix starts the reason (shown as the scenario) of every decision
// a list imports, followed by the list's key.
const ReasonPrefix = "jabali-list:"

// Reason is the decision reason for list key.
func Reason(key string) string { return ReasonPrefix + key }

// Lookup returns the catalog entry for key.
func Lookup(key string) (List, bool) {
	for _, l := range Catalog {
		if l.Key == key {
			return l, true
		}
	}
	return List{}, false
}

// Normalize checks that every key is in the catalog and returns them without
// repeats, in catalog order.
func Normalize(keys []string) ([]string, error) {
	want := map[string]bool{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := Lookup(k); !ok {
			return nil, fmt.Errorf("unknown blocklist %q", k)
		}
		want[k] = true
	}
	out := []string{}
	for _, l := range Catalog {
		if want[l.Key] {
			out = append(out, l.Key)
		}
	}
	return out, nil
}

// MaxCustom is the most custom list URLs the admin may add.
const MaxCustom = 10

// maxCustomURL is the longest custom list URL accepted.
const maxCustomURL = 2048

const customKeyPrefix = "custom-"

// CustomKey names a custom list by its URL, so one URL always has one key and
// the decisions it imported can be found again when the URL is removed.
func CustomKey(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return customKeyPrefix + hex.EncodeToString(sum[:6])
}

// IsCustomKey reports whether key names a custom list rather than a catalog
// one.
func IsCustomKey(key string) bool { return strings.HasPrefix(key, customKeyPrefix) }

// NormalizeCustom checks the admin's custom list URLs and returns them
// trimmed, without blanks or repeats, in the order given. Each must be an
// https URL with a host, no user name or password, at most 2048 characters,
// and, when the host is an address, a public one. At most MaxCustom.
//
// The agent checks again where it downloads: every address the host resolves
// to must be public and not the server's own, on each redirect too.
func NormalizeCustom(urls []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" || seen[u] {
			continue
		}
		if err := checkCustomURL(u); err != nil {
			return nil, fmt.Errorf("%q: %w", u, err)
		}
		seen[u] = true
		out = append(out, u)
	}
	if len(out) > MaxCustom {
		return nil, fmt.Errorf("%d custom lists; at most %d", len(out), MaxCustom)
	}
	return out, nil
}

// ValidCustom is NormalizeCustom for stored URLs: one that no longer passes
// is left out instead of failing the rest, and at most MaxCustom are kept.
func ValidCustom(urls []string) []string {
	out := []string{}
	for _, u := range urls {
		if len(out) == MaxCustom {
			break
		}
		if got, err := NormalizeCustom(append(append([]string{}, out...), u)); err == nil {
			out = got
		}
	}
	return out
}

func checkCustomURL(s string) error {
	if len(s) > maxCustomURL {
		return fmt.Errorf("longer than %d characters", maxCustomURL)
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("contains a space or control character")
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("not a URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("must start with https://")
	}
	if u.User != nil {
		return fmt.Errorf("must not hold a user name or password")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("has no host")
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("must be a public host")
	}
	if a, err := netip.ParseAddr(host); err == nil {
		a = a.Unmap()
		if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsMulticast() || !a.IsGlobalUnicast() {
			return fmt.Errorf("must be a public host")
		}
	}
	return nil
}
