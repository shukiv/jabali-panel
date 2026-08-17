package commands

import (
	"regexp"
	"testing"
)

// JAB-256: the legacy same-uid session kill matches sshd sessions by login
// name (not uid). The pattern MUST be anchored so it kills only the target
// alias's session and never a prefix-sibling's — killing the wrong same-uid
// login is the exact hazard this branch exists to avoid.
func TestFtpSshdSessionPattern(t *testing.T) {
	re := regexp.MustCompile(ftpSshdSessionPattern("shop"))

	mustMatch := []string{
		"sshd: shop@notty",   // SFTP session (no tty)
		"sshd: shop@pts/0",   // interactive
		"sshd: shop [priv]",  // privsep monitor
	}
	for _, s := range mustMatch {
		if !re.MatchString(s) {
			t.Errorf("pattern must match %q", s)
		}
	}

	mustNotMatch := []string{
		"sshd: shop_deploy@notty", // underscore-suffixed sibling at same uid
		"sshd: shopper@notty",     // longer login sharing the prefix
		"sshd: workshop@notty",    // login containing the name as a substring
	}
	for _, s := range mustNotMatch {
		if re.MatchString(s) {
			t.Errorf("pattern must NOT match %q (prefix/substring collision)", s)
		}
	}
}
