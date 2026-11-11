package cpanel

import (
	"strings"
	"testing"
)

// JAB-208. With --preserve-source-state active, a mailbox whose password could
// not be carried printed the generic notice ending "use --preserve-source-state
// … to carry the source password" — advice the operator had already followed.
// During a fleet migration that reads as a flag-plumbing failure and costs a
// re-run of the whole restore to establish that nothing was broken.
//
// A cPanel system-account mailbox authenticates against /etc/shadow, so
// ~/etc/<domain>/shadow carries no entry for it and there is genuinely nothing
// portable to preserve. The fallback is correct; only the guidance was wrong.
func TestMailboxLockoutNotice_DistinguishesNoHashFromFlagOff(t *testing.T) {
	t.Parallel()

	withFlag := mailboxLockoutNotice("shopacct", "shop.example", lockoutNoSourceEntry)
	if strings.Contains(withFlag, "use --preserve-source-state") {
		t.Errorf("must not suggest a flag that is already active:\n%s", withFlag)
	}
	if !strings.Contains(withFlag, "no portable password hash") {
		t.Errorf("should state why preservation was impossible:\n%s", withFlag)
	}
	if !strings.Contains(withFlag, "will not change this") {
		t.Errorf("should say a re-run is pointless — that is the cost being avoided:\n%s", withFlag)
	}

	withoutFlag := mailboxLockoutNotice("shopacct", "shop.example", lockoutPreserveOff)
	if !strings.Contains(withoutFlag, "use --preserve-source-state") {
		t.Errorf("with the flag off the hint is correct and must stay:\n%s", withoutFlag)
	}

	// Both must still name the mailbox and say the owner is locked out — that
	// loudness was itself a fix (a quiet "temp password set" got a locked-out
	// mailbox handed to a tenant).
	for _, msg := range []string{withFlag, withoutFlag} {
		if !strings.Contains(msg, "shopacct@shop.example") || !strings.Contains(msg, "locked out") {
			t.Errorf("notice lost the mailbox identity or the lockout warning:\n%s", msg)
		}
	}
}
