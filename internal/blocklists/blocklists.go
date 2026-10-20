// Package blocklists is the catalog of public IP blocklists the admin can
// add to CrowdSec on top of its community blocklist. panel-api stores and
// shows the choice; the agent fetches each list from the URL here (never
// from a URL the panel sends), drops what must not be banned, and imports
// the rest as CrowdSec ban decisions.
package blocklists

import (
	"fmt"
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
