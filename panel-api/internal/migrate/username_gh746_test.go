package migrate

import "testing"

// GH #746: the validate stage rejected underscore, but the agent creates
// underscore users (looksLikeUnixUsername allows it) and the Plesk auto-kick
// slug produces demolab.test -> demolab_test. The mismatch failed the import
// after the account already existed. Underscore is a valid unix username char.
func TestIsValidUnixUsername_GH746(t *testing.T) {
	valid := []string{"demolab", "demolab_test", "demolab-test", "a", "u1", "wp_7zsxu", "acct1"}
	invalid := []string{
		"",             // empty
		"demolab.test", // dot
		"1abc",         // starts with digit
		"-abc",         // starts with hyphen
		"_abc",         // starts with underscore
		"Demolab",      // uppercase
		"foo bar",      // space
		"toolongusernamethatexceedsthirtytwochars_x", // > 32
	}
	for _, s := range valid {
		if !isValidUnixUsername(s) {
			t.Errorf("expected %q valid", s)
		}
	}
	for _, s := range invalid {
		if isValidUnixUsername(s) {
			t.Errorf("expected %q invalid", s)
		}
	}
}
