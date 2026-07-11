package commands

import (
	"strings"
	"testing"
)

const aptListUpgradableSample = `Listing...
curl/stable 8.5.0-2 amd64 [upgradable from: 8.4.0-2]
libc6/stable 2.36-9+deb12u4 amd64 [upgradable from: 2.36-9+deb12u3]
openssl/stable-security 3.0.13-1~deb12u1 amd64 [upgradable from: 3.0.11-1~deb12u2]
`

const aptListNoUpgradesSample = `Listing...
`

const aptListNoise = `Listing...
WARNING: apt does not have a stable CLI interface. Use with caution in scripts.
`

func TestParseAptUpgradable(t *testing.T) {
	pkgs := parseAptUpgradable(aptListUpgradableSample)
	if len(pkgs) != 3 {
		t.Fatalf("want 3 packages, got %d: %+v", len(pkgs), pkgs)
	}
	if pkgs[0].Name != "curl" {
		t.Errorf("pkgs[0].Name = %q, want curl", pkgs[0].Name)
	}
	if pkgs[0].Current != "8.4.0-2" {
		t.Errorf("pkgs[0].Current = %q, want 8.4.0-2", pkgs[0].Current)
	}
	if pkgs[0].New != "8.5.0-2" {
		t.Errorf("pkgs[0].New = %q, want 8.5.0-2", pkgs[0].New)
	}
	if pkgs[0].Source != "stable" {
		t.Errorf("pkgs[0].Source = %q, want stable", pkgs[0].Source)
	}
	if pkgs[2].Source != "stable-security" {
		t.Errorf("pkgs[2].Source = %q, want stable-security", pkgs[2].Source)
	}
	if pkgs[0].Security {
		t.Errorf("pkgs[0] (curl/stable) Security = true, want false")
	}
	if !pkgs[2].Security {
		t.Errorf("pkgs[2] (openssl/stable-security) Security = false, want true")
	}
}

func TestParseAptUpgradable_SecurityCount(t *testing.T) {
	pkgs := parseAptUpgradable(aptListUpgradableSample)
	n := 0
	for _, p := range pkgs {
		if p.Security {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("security count = %d, want 1", n)
	}
}

func TestParseAptUpgradable_Empty(t *testing.T) {
	pkgs := parseAptUpgradable(aptListNoUpgradesSample)
	if len(pkgs) != 0 {
		t.Fatalf("want 0 packages, got %d", len(pkgs))
	}
}

func TestParseAptUpgradable_IgnoresWarnings(t *testing.T) {
	pkgs := parseAptUpgradable(aptListNoise)
	if len(pkgs) != 0 {
		t.Fatalf("want 0 packages, got %d (warning lines must be skipped)", len(pkgs))
	}
}

// JAB-10: parseAptUpgradable must not crash or invent packages on malformed
// package-manager output (garbage lines, truncated columns).
func TestParseAptUpgradable_Malformed(t *testing.T) {
	garbage := "Listing...\n" +
		"this is not a valid apt line\n" +
		"curl/stable 8.5.0-2\n" + // truncated: no [upgradable from: …]
		"\x00\x01 binary junk \xff\n"
	pkgs := parseAptUpgradable(garbage)
	if len(pkgs) != 0 {
		t.Fatalf("malformed output must parse to 0 packages, got %d: %+v", len(pkgs), pkgs)
	}
}

// JAB-10: classifyAptError maps apt failures to stable, admin-facing reasons.
func TestClassifyAptError(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   string
	}{
		{
			name:   "dpkg lock",
			stderr: "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 4211 (unattended-upgr)",
			want:   "apt_locked",
		},
		{
			name:   "lock timeout exceeded",
			stderr: "E: dpkg::Lock::Timeout of 60 seconds was exceeded",
			want:   "apt_locked",
		},
		{
			name:   "repo dns failure",
			stderr: "Err:1 http://deb.debian.org/debian bookworm InRelease\n  Temporary failure resolving 'deb.debian.org'",
			want:   "repo_unreachable",
		},
		{
			name:   "repo fetch failure",
			stderr: "E: Failed to fetch http://deb.debian.org/… Connection timed out",
			want:   "repo_unreachable",
		},
		{
			name:   "not root",
			stderr: "E: Could not open lock file /var/lib/dpkg/lock-frontend - open (13: Permission denied)\nAre you root?",
			want:   "permission",
		},
		{
			name:   "generic failure",
			stderr: "E: Some packages could not be installed. Unmet dependencies.",
			want:   "command_failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := classifyAptError("apt-get update", tc.stderr, 100, nil)
			if e == nil {
				t.Fatal("classifyAptError returned nil")
			}
			if e.Reason != tc.want {
				t.Errorf("reason = %q, want %q (stderr=%q)", e.Reason, tc.want, tc.stderr)
			}
			if e.Command != "apt-get update" {
				t.Errorf("command = %q, want apt-get update", e.Command)
			}
			if e.ExitCode != 100 {
				t.Errorf("exit_code = %d, want 100", e.ExitCode)
			}
			if e.Hint == "" {
				t.Error("hint must not be empty (admins need a recovery step)")
			}
			if e.Stderr == "" {
				t.Error("stderr summary must not be empty when stderr was provided")
			}
		})
	}
}

// summarizeStderr keeps only the last few lines and caps length so a wall of
// apt output can't blow up the admin card.
func TestSummarizeStderr(t *testing.T) {
	if got := summarizeStderr("  \n\n  "); got != "" {
		t.Errorf("all-blank input = %q, want empty", got)
	}
	many := ""
	for i := 0; i < 20; i++ {
		many += "line\n"
	}
	got := summarizeStderr(many)
	if lines := len(splitNonEmpty(got)); lines > 4 {
		t.Errorf("summary kept %d lines, want <= 4", lines)
	}
	long := ""
	for i := 0; i < 1000; i++ {
		long += "x"
	}
	if got := summarizeStderr(long); len([]rune(got)) > 601 {
		t.Errorf("summary length = %d runes, want <= 601 (600 + ellipsis)", len([]rune(got)))
	}
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
