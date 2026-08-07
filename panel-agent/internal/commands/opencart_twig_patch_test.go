package commands

import (
	"strings"
	"testing"
)

// GH #953: OpenCart's Twig loader stats "/" (open_basedir-denied under jabali's
// per-user confinement). rewriteOpenCartTwigLoader must repoint it at the
// install web root ($this->root) so pages render without relaxing the hardening.
func TestRewriteOpenCartTwigLoader(t *testing.T) {
	const in = "\t\t$this->loader = new \\Twig\\Loader\\FilesystemLoader('/', $this->root);\n"
	out, changed := rewriteOpenCartTwigLoader(in)
	if !changed {
		t.Fatal("expected the hardcoded '/' loader to be rewritten")
	}
	if strings.Contains(out, "FilesystemLoader('/',") {
		t.Errorf("must not stat '/', got: %q", out)
	}
	if !strings.Contains(out, `FilesystemLoader($this->root, $this->root)`) {
		t.Errorf("must point the loader at the web root, got: %q", out)
	}
}

// A shape we don't recognise (already patched / upstream change) is a no-op,
// never a spurious rewrite.
func TestRewriteOpenCartTwigLoader_NoMarkerIsNoop(t *testing.T) {
	for _, in := range []string{
		`$this->loader = new \Twig\Loader\FilesystemLoader($this->root, $this->root);`, // already patched
		`$this->loader = new \Twig\Loader\ArrayLoader([]);`,                            // unrelated
		"",
	} {
		out, changed := rewriteOpenCartTwigLoader(in)
		if changed || out != in {
			t.Errorf("expected no-op for %q, got changed=%v out=%q", in, changed, out)
		}
	}
}
