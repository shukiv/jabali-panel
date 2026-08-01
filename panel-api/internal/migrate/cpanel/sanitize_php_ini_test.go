package cpanel

import (
	"strings"
	"testing"
)

func TestSanitizePhpIni(t *testing.T) {
	in := strings.Join([]string{
		`session.save_path = "/var/cpanel/php/sessions/ea-php83"`,
		`open_basedir = "/home/old:/usr/local/cpanel"`,
		`error_log = /var/cpanel/logs/error_log`,
		`upload_tmp_dir = /home/migdaleinav/tmp`, // already in jail → keep
		`memory_limit = 256M`,                    // benign → keep verbatim
		`max_execution_time = 120`,               // benign → keep
		`error_log = syslog`,                     // not a path → keep
		`extension_dir = "/opt/cpanel/ea-php83/root/usr/lib64/php/modules"`,
	}, "\n")

	out, notes, changed := sanitizePhpIni(in, "migdaleinav", "/home/migdaleinav/domains/x/public_html", func(string) bool { return false })
	if !changed {
		t.Fatal("expected changed=true")
	}
	// session.save_path rewritten to /tmp
	if !strings.Contains(out, "session.save_path = /tmp") {
		t.Errorf("session.save_path not rewritten:\n%s", out)
	}
	// open_basedir dropped
	if strings.Contains(out, "\nopen_basedir = ") || strings.Contains(out, "^open_basedir = ") {
		t.Errorf("open_basedir not removed:\n%s", out)
	}
	if !strings.Contains(out, "jabali-migration: removed open_basedir") {
		t.Errorf("open_basedir removal note missing:\n%s", out)
	}
	// cPanel error_log dropped, but error_log=syslog kept
	if !strings.Contains(out, "removed error_log (path") {
		t.Errorf("cPanel error_log not removed:\n%s", out)
	}
	if !strings.Contains(out, "error_log = syslog") {
		t.Errorf("benign error_log=syslog was clobbered:\n%s", out)
	}
	// extension_dir at cPanel path dropped
	if !strings.Contains(out, "removed extension_dir") {
		t.Errorf("cPanel extension_dir not removed:\n%s", out)
	}
	// benign tunables preserved verbatim
	for _, keep := range []string{"memory_limit = 256M", "max_execution_time = 120", "upload_tmp_dir = /home/migdaleinav/tmp"} {
		if !strings.Contains(out, keep) {
			t.Errorf("benign directive clobbered: %q\n%s", keep, out)
		}
	}
	if len(notes) == 0 {
		t.Error("expected manifest notes")
	}
}

func TestSanitizePhpIni_NoOp(t *testing.T) {
	in := "memory_limit = 512M\nupload_max_filesize = 64M\nsession.save_path = /home/u/sessions\n"
	out, notes, changed := sanitizePhpIni(in, "u", "/home/u/domains/x/public_html", func(string) bool { return true })
	if changed || out != in || notes != nil {
		t.Errorf("clean ini should be untouched; changed=%v notes=%v", changed, notes)
	}
}
