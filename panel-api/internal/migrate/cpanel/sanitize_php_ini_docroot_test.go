package cpanel

import (
	"strings"
	"testing"
)

// JAB-211. Account wineworldc restored green — DB credentials checked out, the
// summary was clean — and every request returned a bare HTTP 500 with an empty
// body. The migrated .user.ini held
//
//	auto_prepend_file = '/home/wineworldc/public_html/malcare-waf.php'
//
// which is the SOURCE cPanel layout. On jabali the docroot is
// /home/<user>/domains/<domain>/public_html, so the file was not there and PHP
// fataled with "Failed opening required" before executing a single line.
//
// Two separate gaps let it through:
//
//  1. auto_prepend_file was not in phpIniPathDirectives, so the loop skipped
//     the line entirely.
//  2. Even listed, it would still have passed: pathInJail reports TRUE for
//     /home/wineworldc/..., because the path IS under the user's home. Jail
//     membership is the wrong question for a prepend file — the right one is
//     whether the file EXISTS. A path can be perfectly in-jail and still fatal
//     the site.
//
// The file itself had been migrated and was present at the new docroot, so the
// sanitizer must remap rather than delete: deleting works around the 500 but
// silently disarms the site's WAF, which is a security regression dressed up
// as a fix.

// destDocroot is the jabali-layout docroot used across these tests.
const destDocroot = "/home/wineworldc/domains/wineworld.co.il/public_html"

// existsOnly builds an exists func that reports true for exactly these paths.
func existsOnly(paths ...string) func(string) bool {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	return func(p string) bool { return set[p] }
}

func TestSanitizePhpIni_RemapsAutoPrependIntoDestDocroot(t *testing.T) {
	t.Parallel()

	in := `auto_prepend_file = '/home/wineworldc/public_html/malcare-waf.php'`
	want := destDocroot + "/malcare-waf.php"

	out, notes, changed := sanitizePhpIni(in, "wineworldc", destDocroot,
		existsOnly(want))

	if !changed {
		t.Fatal("expected changed=true — an unresolvable auto_prepend_file is a " +
			"hard 500 on every request")
	}
	if !strings.Contains(out, want) {
		t.Errorf("auto_prepend_file not remapped to the destination docroot:\n%s", out)
	}
	if strings.Contains(out, "/home/wineworldc/public_html/malcare-waf.php") {
		t.Errorf("source-layout path survived:\n%s", out)
	}
	// Deleting the directive would also stop the 500 — and disable MalCare's
	// WAF. The whole point of remapping is that the file was migrated.
	if strings.Contains(out, "jabali-migration: disabled") {
		t.Errorf("directive was disabled even though the target exists:\n%s", out)
	}
	if len(notes) == 0 {
		t.Error("a rewritten directive must appear in the manifest — parity with " +
			"the existing php_ini_sanitized lines")
	}
}

func TestSanitizePhpIni_DisablesAutoPrependWhenTargetMissing(t *testing.T) {
	t.Parallel()

	// The plugin directory was excluded from the migration, so nothing can be
	// remapped. Leaving the directive would keep the site at 500 forever.
	in := `auto_prepend_file = "/home/wineworldc/public_html/wordfence-waf.php"`

	out, notes, changed := sanitizePhpIni(in, "wineworldc", destDocroot,
		existsOnly( /* nothing exists */ ))

	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(out, "jabali-migration: disabled auto_prepend_file") {
		t.Errorf("unresolvable directive must be commented out, not left to fatal:\n%s", out)
	}
	// Commented out, so PHP never sees it.
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "auto_prepend_file") {
			t.Errorf("directive still live:\n%s", out)
		}
	}
	// This one is a real functional loss (the WAF is now off), so it has to be
	// loud rather than a silent cleanup.
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "wordfence-waf.php") || !strings.Contains(joined, "not migrated") {
		t.Errorf("note must name the missing file and say why it was disabled: %v", notes)
	}
}

func TestSanitizePhpIni_LeavesResolvableAutoPrependAlone(t *testing.T) {
	t.Parallel()

	// Already correct for the jabali layout — must not be touched.
	live := destDocroot + "/guard.php"
	in := "auto_prepend_file = " + live

	out, notes, changed := sanitizePhpIni(in, "wineworldc", destDocroot, existsOnly(live))

	if changed || out != in || notes != nil {
		t.Errorf("a directive that already resolves must be left verbatim; "+
			"changed=%v notes=%v out=%q", changed, notes, out)
	}
}

func TestSanitizePhpIni_AutoAppendGetsSameTreatment(t *testing.T) {
	t.Parallel()

	want := destDocroot + "/footer.php"
	in := `auto_append_file = /home/wineworldc/public_html/footer.php`

	out, _, changed := sanitizePhpIni(in, "wineworldc", destDocroot, existsOnly(want))

	if !changed || !strings.Contains(out, want) {
		t.Errorf("auto_append_file must be remapped like auto_prepend_file:\n%s", out)
	}
}

func TestSanitizePhpIni_RemapsErrorLogDirectory(t *testing.T) {
	t.Parallel()

	// error_log is already in phpIniPathDirectives, but pathInJail says TRUE
	// for the source path, so it survived and PHP then failed to open it. The
	// directory is what has to exist, not the file.
	in := `error_log = /home/wineworldc/public_html/php_errors.log`
	want := destDocroot + "/php_errors.log"

	out, _, changed := sanitizePhpIni(in, "wineworldc", destDocroot, existsOnly(destDocroot))

	if !changed || !strings.Contains(out, want) {
		t.Errorf("in-jail-but-stale error_log path not remapped:\n%s", out)
	}
}

func TestSanitizePhpIni_IncludePathDropsOnlyDeadElements(t *testing.T) {
	t.Parallel()

	// include_path is a list. A dead element is not fatal on its own, but it
	// costs a failed stat on every include, and the live elements must survive
	// untouched — including the relative "." entry.
	live := destDocroot + "/lib"
	in := `include_path = ".:/home/wineworldc/public_html/lib:/home/wineworldc/public_html/gone"`

	out, notes, changed := sanitizePhpIni(in, "wineworldc", destDocroot, existsOnly(live))

	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(out, live) {
		t.Errorf("remappable include_path element was lost:\n%s", out)
	}
	if !strings.Contains(out, ".") {
		t.Errorf("relative element must be preserved:\n%s", out)
	}
	if strings.Contains(out, "/public_html/gone") {
		t.Errorf("unresolvable element kept:\n%s", out)
	}
	if len(notes) == 0 {
		t.Error("expected a manifest note for the rewritten include_path")
	}
}

// The docroot-aware pass must not disturb anything the original sanitizer did.
func TestSanitizePhpIni_ExistingBehaviourUnchanged(t *testing.T) {
	t.Parallel()

	in := strings.Join([]string{
		`session.save_path = "/var/cpanel/php/sessions/ea-php83"`,
		`open_basedir = "/home/old:/usr/local/cpanel"`,
		`memory_limit = 256M`,
	}, "\n")

	out, _, changed := sanitizePhpIni(in, "migdaleinav", "/home/migdaleinav/domains/x/public_html",
		existsOnly())

	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(out, "session.save_path = /tmp") {
		t.Errorf("session.save_path regressed:\n%s", out)
	}
	if !strings.Contains(out, "jabali-migration: removed open_basedir") {
		t.Errorf("open_basedir regressed:\n%s", out)
	}
	if !strings.Contains(out, "memory_limit = 256M") {
		t.Errorf("benign tunable clobbered:\n%s", out)
	}
}

// An empty docroot (caller could not determine one) must degrade to the old
// behaviour rather than remapping onto "/…".
func TestSanitizePhpIni_NoDocrootDoesNotInventPaths(t *testing.T) {
	t.Parallel()

	in := `auto_prepend_file = /home/wineworldc/public_html/malcare-waf.php`

	out, _, changed := sanitizePhpIni(in, "wineworldc", "", existsOnly())

	if changed && strings.Contains(out, "auto_prepend_file = /malcare-waf.php") {
		t.Errorf("empty docroot produced a bogus absolute path:\n%s", out)
	}
}
