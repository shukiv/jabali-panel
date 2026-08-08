package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /etc/jabali-panel/hostname.env holds values that came from a REMOTE service
// (api.jabalihosted.com /v1/claim) and is read by scripts that run as root on
// a timer and at every certbot renewal. Two rules keep that safe, and both are
// pinned here because either one alone is a root-code-execution bug away:
//
//  1. No reader may `source` the file. `source` executes shell metacharacters
//     in a value, so a token containing $(...) or backticks would run as root
//     on every heartbeat, forever, and re-run after every reboot.
//  2. Every writer must reject values outside a strict charset, so nothing
//     dangerous is written in the first place.

func TestHostnameEnvReadersDoNotSourceIt(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("..", "..", "..", "install", "hostname")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		src := string(body)
		if !strings.Contains(src, "hostname.env") {
			continue
		}
		checked++
		for _, bad := range []string{`source "$ENV"`, `. "$ENV"`, "source /etc/jabali-panel/hostname.env"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s executes hostname.env via %q. Values in that file come "+
					"from a remote service and this script runs as root — a token "+
					"containing $(...) would be root code execution. Parse the file "+
					"instead (see jh_env).", e.Name(), bad)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no hostname.env readers found — the layout changed and this test " +
			"is no longer checking anything")
	}
}

// The agent writer must reject a token carrying shell metacharacters. The
// original check only excluded "\n\r ", which still permitted $(...).
func TestFreeTokenRegexRejectsShellMetacharacters(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{
		"$(id)",
		"`id`",
		"${IFS}",
		"tok;reboot",
		"tok\nEXTRA=1",
		"tok with space",
		"tok\"quote",
		"tok'quote",
		"tok|pipe",
		"tok&bg",
		"", // empty
	} {
		if freeTokenRegex.MatchString(bad) {
			t.Errorf("freeTokenRegex accepted %q — that value reaches a file read by root scripts", bad)
		}
	}
	for _, good := range []string{
		"abc123",
		"a-b_c",
		strings.Repeat("f", 64),
	} {
		if !freeTokenRegex.MatchString(good) {
			t.Errorf("freeTokenRegex rejected a legitimate token %q", good)
		}
	}
}
