package api

import "testing"

func TestSanitizeDBLabel(t *testing.T) {
	cases := map[string]string{
		"prod.domain.com":   "prod_domain_com",
		"Domain.COM":        "domain_com",
		"a--b..c":           "a_b_c",
		"_lead.trail_":      "lead_trail",
		"my-sub":            "my_sub",
		"x.y.z.example.org": "x_y_z_example_org",
	}
	for in, want := range cases {
		if got := sanitizeDBLabel(in); got != want {
			t.Errorf("sanitizeDBLabel(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFitDBName(t *testing.T) {
	// Short — unchanged.
	if got := fitDBName("terry_wp_", "domain_com", "_db"); got != "terry_wp_domain_com_db" {
		t.Errorf("short: %q", got)
	}
	// Subdir included.
	if got := fitDBName("terry_wp_", "domain_com_sub", "_user"); got != "terry_wp_domain_com_sub_user" {
		t.Errorf("subdir: %q", got)
	}
	// Long FQDN — truncated to fit 64, suffix preserved.
	long := "averylongsubdomain_segment_that_keeps_going_and_going_example_com_extra"
	got := fitDBName("terry_wp_", long, "_db")
	if len(got) > 64 {
		t.Errorf("len=%d > 64: %q", len(got), got)
	}
	if got[:9] != "terry_wp_" || got[len(got)-3:] != "_db" {
		t.Errorf("prefix/suffix lost: %q", got)
	}
}

// GH #869: app DB/user identifiers are short — <osuser>_<token>_<tail> —
// not the FQDN-based GH #196 scheme that produced 50+ char names.
func TestShortAppDBName(t *testing.T) {
	if got := shortAppDBName("notary", "wordpress", "x4f"); got != "notary_wp_x4f" {
		t.Errorf("wordpress: %q, want notary_wp_x4f", got)
	}
	if got := shortAppDBName("notary", "flarum", "k2p"); got != "notary_flarum_k2p" {
		t.Errorf("flarum: %q, want notary_flarum_k2p", got)
	}
	// OS usernames pass through the same sanitiser as everything else.
	if got := shortAppDBName("Notary.User", "drupal", "9aa"); got != "notary_user_drupal_9aa" {
		t.Errorf("sanitised osuser: %q", got)
	}
	// Even an absurdly long OS user stays inside the 64-char identifier limit.
	longUser := "averyveryverylongsystemusernamethatkeepsgoingandgoingandgoingyes"
	if got := shortAppDBName(longUser, "wordpress", "x4f"); len(got) > 64 {
		t.Errorf("len=%d > 64: %q", len(got), got)
	}
}

// TestAppDBToken — GH #215: each app's DB/user name carries its OWN
// token, not a blanket "wp". WordPress stays "wp" for continuity.
func TestAppDBToken(t *testing.T) {
	cases := map[string]string{
		"wordpress":  "wp",
		"":           "wp",
		"flarum":     "flarum",
		"drupal":     "drupal",
		"phpbb":      "phpbb",
		"prestashop": "prestashop",
		"opencart":   "opencart",
	}
	for in, want := range cases {
		if got := appDBToken(in); got != want {
			t.Errorf("appDBToken(%q) = %q, want %q", in, got, want)
		}
	}
}
