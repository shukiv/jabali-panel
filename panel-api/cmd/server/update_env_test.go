package main

import (
	"strings"
	"testing"
)

// pathValue extracts the PATH= entry from an env slice as appendGoPath returns
// it, splitting into segments for assertions.
func pathValue(t *testing.T, env []string) []string {
	t.Helper()
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			return strings.Split(e[len("PATH="):], ":")
		}
	}
	t.Fatalf("no PATH= entry in env: %v", env)
	return nil
}

func hasSeg(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func countSeg(ss []string, want string) int {
	n := 0
	for _, s := range ss {
		if s == want {
			n++
		}
	}
	return n
}

// TestAppendGoPath_GuaranteesSystemSbins is the regression guard for GH #1773:
// on a box whose operator PATH omits /usr/sbin, the update's nginx/sshd steps
// exited 127 and aborted the whole update before the binary swapped. The child
// PATH appendGoPath builds must always carry the system sbin dirs so those
// bare-name tools resolve.
func TestAppendGoPath_GuaranteesSystemSbins(t *testing.T) {
	t.Setenv("JABALI_GO_ROOT", "/x/go")

	t.Run("operator PATH without sbin gains all three sbin dirs", func(t *testing.T) {
		got := pathValue(t, appendGoPath([]string{"PATH=/usr/bin:/bin", "HOME=/root"}))
		for _, sbin := range []string{"/usr/local/sbin", "/usr/sbin", "/sbin"} {
			if !hasSeg(got, sbin) {
				t.Errorf("PATH %v missing %s", got, sbin)
			}
		}
		// Go bin stays first so the right `go` is found.
		if len(got) == 0 || got[0] != "/x/go/bin" {
			t.Errorf("PATH %v does not start with /x/go/bin", got)
		}
		// Operator's own dirs are preserved.
		if !hasSeg(got, "/usr/bin") || !hasSeg(got, "/bin") {
			t.Errorf("PATH %v dropped an operator dir", got)
		}
	})

	t.Run("an sbin dir already present is not duplicated", func(t *testing.T) {
		got := pathValue(t, appendGoPath([]string{"PATH=/usr/sbin:/usr/bin"}))
		if c := countSeg(got, "/usr/sbin"); c != 1 {
			t.Errorf("/usr/sbin appears %d times in %v, want 1", c, got)
		}
	})

	t.Run("env with no PATH still gets the sbin dirs", func(t *testing.T) {
		got := pathValue(t, appendGoPath([]string{"HOME=/root"}))
		for _, sbin := range []string{"/usr/local/sbin", "/usr/sbin", "/sbin"} {
			if !hasSeg(got, sbin) {
				t.Errorf("fallback PATH %v missing %s", got, sbin)
			}
		}
		if len(got) == 0 || got[0] != "/x/go/bin" {
			t.Errorf("fallback PATH %v does not start with /x/go/bin", got)
		}
	})
}

// TestEnsureSystemSbins covers the helper directly: dedup, empty-segment drop
// (so a stray "" never becomes a "." current-dir entry), and append-at-end
// ordering.
func TestEnsureSystemSbins(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"appends missing sbins in order", "/usr/bin:/bin", "/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin"},
		{"keeps present sbin, appends the rest once", "/usr/sbin:/usr/bin", "/usr/sbin:/usr/bin:/usr/local/sbin:/sbin"},
		{"drops empty segments (no leading dot-in-PATH)", ":/usr/bin::/bin:", "/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin"},
		{"dedupes a repeated dir", "/usr/bin:/usr/bin:/sbin", "/usr/bin:/sbin:/usr/local/sbin:/usr/sbin"},
		{"empty input yields just the sbins", "", "/usr/local/sbin:/usr/sbin:/sbin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ensureSystemSbins(tc.in); got != tc.want {
				t.Errorf("ensureSystemSbins(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
