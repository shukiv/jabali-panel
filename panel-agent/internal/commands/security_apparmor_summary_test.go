package commands

import "testing"

// JAB-379: buildApparmorProfileList is the single source of the jabali-profile
// filtering shared by security.apparmor.status (with denial scrape) and the
// lightweight security.apparmor.summary (dashboard degraded-alert source). Pin
// the hard-won special cases: non-jabali skip, `//` child-shadow skip,
// jabali-ssh-shell unconfined-by-design skip, and missing-profile reporting.
func TestBuildApparmorProfileList(t *testing.T) {
	raw := map[string]string{
		"jabali-bulwark":                     "enforce",
		"jabali-panel":                       "complain",
		"stalwart-mail":                      "enforce",
		"jabali-panel//null-/usr/sbin/other": "complain", // child shadow — skip
		"jabali-ssh-shell":                   "unconfined", // by-design — skip
		"mariadbd":                           "enforce",    // non-jabali — skip
		"jabali-fpm-app":                     "enforce",
	}

	profiles, _ := buildApparmorProfileList(raw)

	got := map[string]string{}
	for _, p := range profiles {
		got[p.Name] = p.Mode
	}

	// Kept, with their real modes.
	for name, wantMode := range map[string]string{
		"jabali-bulwark": "enforce",
		"jabali-panel":   "complain",
		"stalwart-mail":  "enforce",
		"jabali-fpm-app": "enforce",
	} {
		if got[name] != wantMode {
			t.Errorf("profile %q: want mode %q, got %q", name, wantMode, got[name])
		}
	}

	// Skipped entirely.
	for _, name := range []string{
		"jabali-panel//null-/usr/sbin/other",
		"jabali-ssh-shell",
		"mariadbd",
	} {
		if _, ok := got[name]; ok {
			t.Errorf("profile %q must be filtered out, but appeared as %q", name, got[name])
		}
	}
}

// An allowlisted profile absent from aa-status is reported (GH #679) — as
// "missing" (genuine failure) or "kernel-gated" (deliberate skip on kernels
// without unix mediation). Which one is host-dependent (reads /sys), so assert
// the row exists with one of those two modes rather than pinning the kernel.
func TestBuildApparmorProfileList_AbsentProfileReported(t *testing.T) {
	// jabali-bulwark present; the other allowlisted profiles are absent.
	profiles, _ := buildApparmorProfileList(map[string]string{
		"jabali-bulwark": "enforce",
	})

	got := map[string]string{}
	for _, p := range profiles {
		got[p.Name] = p.Mode
	}

	for _, name := range []string{"jabali-panel", "stalwart-mail", "jabali-fpm-app"} {
		mode, ok := got[name]
		if !ok {
			t.Errorf("absent allowlisted profile %q must be reported, not omitted", name)
			continue
		}
		if mode != "missing" && mode != "kernel-gated" {
			t.Errorf("absent profile %q: want missing|kernel-gated, got %q", name, mode)
		}
	}
}
