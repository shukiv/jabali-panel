package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// install.sh converges Stalwart's built-in User role on every install and
// update (GH #1581). The mail server checks a mailbox's API keys itself, not
// against the panel, so they kept working after the panel changed the
// password, disabled the mailbox or suspended its owner; the panel offers no
// use for them, so the role may not create or use them. App passwords stay
// allowed: the webmail's sign-in from the panel ("Open webmail", admin
// impersonation) creates one for the mailbox through the master user, and
// the panel removes them itself when it changes a password.

var shPrincPatchRe = regexp.MustCompile(`local princ_patch='([^']*)'`)

func userRoleDisabledPermissions(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRootT(t), "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	m := shPrincPatchRe.FindSubmatch(raw)
	if m == nil {
		t.Fatal("install.sh: no local princ_patch='...'")
	}
	var patch struct {
		DisabledPermissions map[string]bool `json:"disabledPermissions"`
	}
	if err := json.Unmarshal(m[1], &patch); err != nil {
		t.Fatalf("princ_patch is not JSON: %v", err)
	}
	return patch.DisabledPermissions
}

func TestUserRole_DisablesAPIKeys(t *testing.T) {
	got := userRoleDisabledPermissions(t)
	for _, p := range []string{"sysApiKeyCreate", "sysApiKeyUpdate", "sysApiKeyGet", "sysApiKeyQuery", "sysApiKeyDestroy"} {
		if !got[p] {
			t.Errorf("User role does not disable %s", p)
		}
	}
}

// The GH #1581 enumeration lockdown stays: the patch replaces the whole set.
func TestUserRole_KeepsThePrincipalEnumerationLockdown(t *testing.T) {
	got := userRoleDisabledPermissions(t)
	for _, p := range []string{"jmapPrincipalQuery", "jmapPrincipalQueryChanges", "jmapPrincipalChanges",
		"davPrincipalList", "davPrincipalMatch", "davPrincipalSearch", "davPrincipalSearchPropSet"} {
		if !got[p] {
			t.Errorf("User role does not disable %s", p)
		}
	}
}

// Disabling app passwords would break every webmail sign-in from the panel.
func TestUserRole_LeavesAppPasswordsEnabled(t *testing.T) {
	for p, off := range userRoleDisabledPermissions(t) {
		if off && strings.HasPrefix(p, "sysAppPassword") {
			t.Errorf("User role disables %s: the panel's webmail sign-in creates an app password", p)
		}
	}
}
