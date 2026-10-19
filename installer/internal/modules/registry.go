// Package modules is the single source of truth for the optional Jabali
// modules an operator can select at install time (M353 / GH #353). The
// installer TUI, the non-interactive JABALI_MODULES contract, and (later) the
// panel's Server → Modules page all read this list so the three never drift.
package modules

// Tier classifies whether a module can be turned off.
type Tier int

const (
	// Core modules are always installed and never selectable-off (nginx,
	// MariaDB, the panel + Kratos auth). They are not listed in the registry —
	// the registry is the OPTIONAL set. Tier exists for future use.
	TierCore Tier = iota
	TierRecommended
	TierOptional
)

// Module is one selectable unit.
type Module struct {
	Key       string   // stable key; also the JABALI_MODULES token
	Title     string   // shown in the TUI
	Desc      string   // one-line help
	Tier      Tier     // recommended (pre-ticked) vs optional
	Requires  []string // module keys that must also be enabled
	Conflicts []string // mutually-exclusive module keys
}

// Registry is the ordered list of optional modules. Order = display order.
var Registry = []Module{
	{Key: "dns", Title: "DNS server (PowerDNS)", Desc: "Authoritative DNS + per-domain record management.", Tier: TierRecommended},
	// Mail's install stops without the DNS module (GH #2056).
	{Key: "mail", Title: "Mail server (Stalwart + Bulwark)", Desc: "Mailboxes, forwarders, webmail (needs DNS).", Tier: TierOptional, Requires: []string{"dns"}},
	{Key: "security", Title: "Security suite", Desc: "CrowdSec IDS + malware scanning (LMD, YARA-X) + AppArmor.", Tier: TierRecommended, Conflicts: []string{"fail2ban"}},
	{Key: "docker", Title: "Docker engine", Desc: "Container runtime + the admin's 1-click Docker apps.", Tier: TierOptional},
	{Key: "docker_apps", Title: "Docker apps for users", Desc: "Hosting users run 1-click Docker apps (needs Docker).", Tier: TierOptional, Requires: []string{"docker"}},
	{Key: "python_apps", Title: "Python apps", Desc: "Per-user Python app runtime.", Tier: TierOptional},
	{Key: "postgres", Title: "PostgreSQL", Desc: "PostgreSQL engine alongside MariaDB.", Tier: TierOptional},
	{Key: "quota", Title: "Filesystem quota", Desc: "Per-user disk-quota enforcement.", Tier: TierRecommended},
	{Key: "api", Title: "REST API (API keys)", Desc: "Remote-management API + tokens.", Tier: TierOptional},
}

// byKey indexes the registry for quick lookup.
func byKey(key string) (Module, bool) {
	for _, m := range Registry {
		if m.Key == key {
			return m, true
		}
	}
	return Module{}, false
}

// Keys returns every optional module key in registry order.
func Keys() []string {
	out := make([]string, len(Registry))
	for i, m := range Registry {
		out[i] = m.Key
	}
	return out
}
