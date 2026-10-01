package main

import (
	"errors"
	"testing"
)

// A stable host whose settings read fails during the nightly update must stay
// on its current build. It used to fall back to "development" and reset to
// unreviewed main.
func TestUpdateBaseRef_UnreadableChannelStaysOnCurrentBuild(t *testing.T) {
	for _, channel := range []string{"", "stable", "development"} {
		ref, followStable := updateBaseRef(channel, errors.New("open database: connection refused"))
		if ref != "HEAD" || followStable {
			t.Errorf("updateBaseRef(%q, err) = (%q, %v), want (\"HEAD\", false)", channel, ref, followStable)
		}
	}
}

func TestUpdateBaseRef_Channels(t *testing.T) {
	if ref, follow := updateBaseRef("development", nil); ref != "origin/main" || follow {
		t.Errorf("development = (%q, %v), want (\"origin/main\", false)", ref, follow)
	}
	if _, follow := updateBaseRef("stable", nil); !follow {
		t.Error("stable must follow the `stable` tag")
	}
}

func TestParseReleaseChannel(t *testing.T) {
	for _, ok := range []string{"stable", "development"} {
		if got, err := parseReleaseChannel(ok); err != nil || got != ok {
			t.Errorf("parseReleaseChannel(%q) = (%q, %v)", ok, got, err)
		}
	}
	// An unknown value is not "development": treating it so would move a host
	// to main on a typo or a corrupt row.
	for _, bad := range []string{"", "Stable", "main", "dev"} {
		if got, err := parseReleaseChannel(bad); err == nil {
			t.Errorf("parseReleaseChannel(%q) = %q, want an error", bad, got)
		}
	}
}
