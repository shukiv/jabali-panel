package modules

// Profile is a named preset that pre-selects a set of optional modules, so the
// TUI can offer a quick starting point instead of a wall of checkboxes.
type Profile struct {
	Key     string
	Title   string
	Desc    string
	Modules []string // optional-module keys this profile turns on
}

// Profiles are the deploy presets (Plesk-style), chosen before the checklist.
var Profiles = []Profile{
	{Key: "minimal", Title: "Minimal", Desc: "Web + database + panel only — a few PHP/MySQL sites.", Modules: []string{}},
	{Key: "webhost", Title: "Web host", Desc: "Minimal + DNS + security + quota (typical shared host; the default for a non-interactive install).", Modules: []string{"dns", "security", "quota"}},
	{Key: "mail", Title: "Mail server", Desc: "Web + DNS + mail + security.", Modules: []string{"dns", "mail", "security"}},
	{Key: "full", Title: "Full", Desc: "Everything — mail, Docker, Python apps, PostgreSQL, API.", Modules: allOptionalKeys()},
	{Key: "custom", Title: "Custom", Desc: "Start from Web host and adjust in the checklist.", Modules: []string{"dns", "security", "quota"}},
}

func allOptionalKeys() []string { return Keys() }

// ProfileByKey looks up a profile.
func ProfileByKey(key string) (Profile, bool) {
	for _, p := range Profiles {
		if p.Key == key {
			return p, true
		}
	}
	return Profile{}, false
}
