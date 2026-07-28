package main

import "testing"

// GH #727: on a Custom / no-mail install the jabali-mail group never exists, so
// the mail convergence buildSteps (stalwart binary, spam-filter rules,
// mailhook, bulwark) must be skipped on `jabali update` — otherwise
// _install_spam_rules dies at `install -g jabali-mail`.
func TestGroupExists_GH727(t *testing.T) {
	// The root group exists on every Linux host the updater runs on.
	if !groupExists("root") {
		t.Error("expected the root group to exist")
	}
	// A group name that cannot exist -> false: this is the no-mail host case
	// that used to let the mail buildSteps run and crash.
	if groupExists("jabali-no-such-group-gh727-xyz") {
		t.Error("expected a bogus group to be reported absent")
	}
}

func TestMailModuleInstalled_TracksJabaliMailGroup_GH727(t *testing.T) {
	if mailModuleInstalled() != groupExists("jabali-mail") {
		t.Error("mailModuleInstalled must track presence of the jabali-mail group")
	}
}
