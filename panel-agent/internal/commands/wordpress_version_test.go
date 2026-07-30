package commands

import "testing"

// JAB-180 (supersedes the GH #456 hard pin): the WordPress core version is now
// caller-chosen — "latest" (default) or a concrete X / X.Y / X.Y.Z. The
// validation regex must accept those and reject anything that could smuggle
// extra wp-cli args or shell metacharacters into `wp core download --version=`.
func TestWordPressVersionRe(t *testing.T) {
	valid := []string{"latest", "6", "6.7", "6.7.1", "10.0.3"}
	for _, v := range valid {
		if !wordpressVersionRe.MatchString(v) {
			t.Errorf("version %q should be valid", v)
		}
	}
	invalid := []string{
		"", "Latest", "nightly", "6.x", "6.7.1.2", "6..7",
		"latest;rm -rf /", "6.7 --allow-root", "../6.7", "6.7\n", " 6.7",
	}
	for _, v := range invalid {
		if wordpressVersionRe.MatchString(v) {
			t.Errorf("version %q should be rejected", v)
		}
	}
}

// The locale is passed as a positional arg to wp-cli (Step 3c), so it must
// reject anything that could smuggle a flag / metacharacters while still
// accepting every real WordPress locale shape.
func TestWordPressLocaleRe(t *testing.T) {
	valid := []string{"en_US", "he_IL", "pt_BR", "es_419", "de_DE_formal", "ckb", "zh_CN"}
	for _, v := range valid {
		if !wordpressLocaleRe.MatchString(v) {
			t.Errorf("locale %q should be valid", v)
		}
	}
	invalid := []string{
		"", "-", "--require=/tmp/x.php", "-rf", "en US", "en;rm", "../en",
		"en_US ", "en=US", "en\nUS", "e",
	}
	for _, v := range invalid {
		if wordpressLocaleRe.MatchString(v) {
			t.Errorf("locale %q should be rejected", v)
		}
	}
}
