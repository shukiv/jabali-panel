package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// shippedProfileDir is the repo's install/apparmor, which install.sh copies
// to /etc/apparmor.d/ under the same file names.
const shippedProfileDir = "../../../install/apparmor"

// topLevelProfile matches a profile declared at column 0. Nested `profile`
// blocks (hats, child profiles inside a parent's braces) are indented and
// can't be flipped on their own anyway.
var topLevelProfile = regexp.MustCompile(`(?m)^profile\s+(\S+)`)

// shippedProfiles maps each shipped profile file to the profiles it declares.
func shippedProfiles(t *testing.T) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir(shippedProfileDir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".disabled") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(shippedProfileDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range topLevelProfile.FindAllStringSubmatch(string(body), -1) {
			files[e.Name()] = append(files[e.Name()], m[1])
		}
	}
	if len(files) == 0 {
		t.Fatalf("no profiles found under %s", shippedProfileDir)
	}
	return files
}

// GH #2001: aa-enforce and aa-complain change every profile in the file they
// are given. A shipped profile that shares its file with another, or that the
// agent doesn't map to its file, can't be switched from Security -> AppArmor:
// the switch fails with "profile not in allowlist" or flips its neighbour too.
func TestShippedApparmorProfiles_EachCanBeSwitchedOnItsOwn(t *testing.T) {
	files := shippedProfiles(t)

	for file, labels := range files {
		if len(labels) != 1 {
			t.Errorf("%s declares %d profiles %v; aa-enforce/aa-complain work per file, so each profile needs its own file", file, len(labels), labels)
		}
		for _, label := range labels {
			if !allowedProfiles[label] {
				t.Errorf("profile %q (%s) is not in allowedProfiles, so the panel can't switch its mode", label, file)
			}
			if got, want := apparmorProfileFile(label), "/etc/apparmor.d/"+file; got != want {
				t.Errorf("apparmorProfileFile(%q) = %q, want %q", label, got, want)
			}
		}
	}

	// And the other way round: every profile the panel offers ships in the file
	// the agent hands to aa-enforce/aa-complain.
	for name := range allowedProfiles {
		path := apparmorProfileFile(name)
		labels, ok := files[filepath.Base(path)]
		if !ok {
			t.Errorf("allowlisted profile %q maps to %q, which install/apparmor doesn't ship", name, path)
			continue
		}
		if len(labels) != 1 || labels[0] != name {
			t.Errorf("allowlisted profile %q maps to %q, which declares %v", name, path, labels)
		}
	}
}

// The mode switch for jabali-sendmail runs aa-enforce/aa-complain on the
// sendmail file only, and the switch for jabali-fpm-app on the fpm-exec file
// only, so each one changes without the other.
func TestApparmorSetMode_SendmailAndFpmAppSwitchSeparately(t *testing.T) {
	for _, tc := range []struct {
		profile, mode string
		want          []string
	}{
		{"jabali-sendmail", "enforce", []string{"aa-enforce", "/etc/apparmor.d/usr.local.libexec.jabali.jabali-sendmail"}},
		{"jabali-sendmail", "complain", []string{"aa-complain", "/etc/apparmor.d/usr.local.libexec.jabali.jabali-sendmail"}},
		{"jabali-fpm-app", "complain", []string{"aa-complain", "/etc/apparmor.d/usr.local.libexec.jabali.fpm-exec"}},
	} {
		calls := captureExec(t)
		raw, _ := json.Marshal(apparmorSetModeRequest{Profile: tc.profile, Mode: tc.mode})
		if _, err := mwApparmorSetModeHandler(context.Background(), raw); err != nil {
			t.Fatalf("%s -> %s: %v", tc.profile, tc.mode, err)
		}
		if len(*calls) != 1 || strings.Join((*calls)[0], " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s -> %s ran %v, want exactly %v", tc.profile, tc.mode, *calls, tc.want)
		}
	}
}
