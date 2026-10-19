package main

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// avcLine builds an auditd AppArmor record like the kernel writes it.
func avcLine(sec int64, serial int, status, profile, name string) string {
	return fmt.Sprintf(`type=AVC msg=audit(%d.123:%d): apparmor="%s" operation="open" class="file" profile="%s" name="%s" pid=42 comm="x" requested_mask="w" denied_mask="w" fsuid=0 ouid=0`,
		sec, serial, status, profile, name)
}

// fakeJournalctl puts a journalctl on PATH that prints output, exits with
// exit, and records its arguments (one per line) in the returned file.
func fakeJournalctl(t *testing.T, output string, exit int) string {
	t.Helper()
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out")
	argsFile := filepath.Join(dir, "args")
	if err := os.WriteFile(outFile, []byte(output), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\ncat '%s'\nexit %d\n", argsFile, outFile, exit)
	if err := os.WriteFile(filepath.Join(dir, "journalctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

// withAuditLogs points the audit log glob at a temp dir holding files (a
// ".gz" name is written gzip-compressed).
func withAuditLogs(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if !strings.HasSuffix(name, ".gz") {
			if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
				t.Fatal(err)
			}
			continue
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		if _, err := gz.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	old := apparmorAuditLogGlob
	apparmorAuditLogGlob = filepath.Join(dir, "audit.log*")
	t.Cleanup(func() { apparmorAuditLogGlob = old })
	return dir
}

func countText(t *testing.T, text, profile string) (int, string) {
	t.Helper()
	n, sample, err := countApparmorViolations(strings.NewReader(text), profile, time.Time{}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	return n, sample
}

// JAB-17: the flip-mature soak gate must recognise a still-violating profile
// (so it refuses to flip it into the #705 crash-loop) and must not
// cross-match a different profile's events.
func TestCountApparmorViolations(t *testing.T) {
	log := `Jul 12 00:01:02 mx kernel: audit: type=1400 audit(1.2:3): apparmor="DENIED" operation="open" profile="jabali-panel" name="/etc/secret" pid=42 comm="jabali-panel" requested_mask="r" denied_mask="r"
Jul 12 00:01:03 mx kernel: audit: type=1400 audit(1.2:4): apparmor="ALLOWED" operation="open" profile="jabali-panel" name="/ok"
Jul 12 00:02:00 mx kernel: audit: type=1400 audit(1.2:5): apparmor="DENIED" operation="connect" profile="jabali-panel" name="/run/x.sock"
Jul 12 00:02:30 mx kernel: audit: type=1400 audit(1.2:7): apparmor="ALLOWED" operation="exec" profile="jabali-panel//null-/usr/bin/df" name="/usr/bin/df"
Jul 12 00:02:40 mx kernel: audit: type=1400 audit(1.2:8): apparmor="STATUS" operation="profile_replace" profile="jabali-panel" name="jabali-panel"
Jul 12 00:03:00 mx kernel: audit: type=1400 audit(1.2:6): apparmor="DENIED" operation="exec" profile="stalwart-mail" name="/usr/bin/foo"`

	// jabali-panel: 2 DENIED + 1 ALLOWED. The child profile's line, the
	// STATUS line and the stalwart line don't count.
	n, sample := countText(t, log, "jabali-panel")
	if n != 3 {
		t.Fatalf("jabali-panel violations = %d, want 3", n)
	}
	if sample == "" {
		t.Error("expected a sample line")
	}

	// A different profile's events must not count toward jabali-panel.
	if n, _ := countText(t, log, "jabali-bulwark"); n != 0 {
		t.Errorf("jabali-bulwark violations = %d, want 0 (no cross-match)", n)
	}

	// stalwart-mail has exactly one.
	if n, _ := countText(t, log, "stalwart-mail"); n != 1 {
		t.Errorf("stalwart-mail violations = %d, want 1", n)
	}

	// No AppArmor event for the profile → zero → soak-clean → flip allowed.
	clean := `Jul 12 00:01:02 mx kernel: audit: apparmor="STATUS" operation="profile_load" profile="jabali-panel" name="jabali-panel"
Jul 12 00:01:03 mx kernel: eth0: link up`
	if n, _ := countText(t, clean, "jabali-panel"); n != 0 {
		t.Errorf("clean log violations = %d, want 0", n)
	}
}

// A complain-mode profile never logs DENIED for its own rules: every access
// it would deny is logged as ALLOWED. Those are exactly the accesses that
// EACCES once the profile is enforced, so they must keep it from flipping.
func TestCountApparmorViolations_ComplainAllowedCounts(t *testing.T) {
	line := avcLine(time.Now().Unix(), 9, "ALLOWED", "stalwart-mail", "/srv/blob/x")
	if n, _ := countText(t, line, "stalwart-mail"); n != 1 {
		t.Fatalf("complain-mode ALLOWED counted %d, want 1", n)
	}
}

func TestCountApparmorViolations_WindowAndDedupe(t *testing.T) {
	now := time.Now()
	recent, old := now.Add(-time.Hour).Unix(), now.Add(-10*24*time.Hour).Unix()
	text := strings.Join([]string{
		avcLine(old, 1, "DENIED", "stalwart-mail", "/old"),
		avcLine(recent, 2, "ALLOWED", "stalwart-mail", "/new"),
		avcLine(recent, 2, "ALLOWED", "stalwart-mail", "/new"), // same record again
	}, "\n")
	n, sample, err := countApparmorViolations(strings.NewReader(text), "stalwart-mail", now.Add(-7*24*time.Hour), map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || !strings.Contains(sample, `name="/new"`) {
		t.Fatalf("got %d (%q), want 1: the old record is outside the window, the repeat is the same record", n, sample)
	}
}

// With auditd running, AppArmor records land in /var/log/audit and never in
// the kernel journal: the gate must read the audit log, rotations included.
func TestApparmorRecentViolations_ReadsTheAuditLog(t *testing.T) {
	argsFile := fakeJournalctl(t, "", 0)
	now := time.Now()
	recent, old := now.Add(-time.Hour).Unix(), now.Add(-10*24*time.Hour).Unix()
	withAuditLogs(t, map[string]string{
		"audit.log": avcLine(recent, 10, "ALLOWED", "stalwart-mail", "/srv/blob/a") + "\n" +
			avcLine(recent, 11, "DENIED", "jabali-panel", "/other-profile"),
		"audit.log.1":    avcLine(old, 5, "ALLOWED", "stalwart-mail", "/before-the-window"),
		"audit.log.2.gz": avcLine(recent, 12, "DENIED", "stalwart-mail", "/rotated-and-compressed"),
	})

	n, sample, err := apparmorRecentViolations("stalwart-mail", 7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("violations = %d, want 2 (audit.log + the compressed rotation; the old record and the other profile don't count)", n)
	}
	if sample == "" {
		t.Error("expected a sample line")
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(args)), "\n")
	want := []string{"--no-pager", "-q", "--since", "7 days ago", "_TRANSPORT=kernel", "+", "_TRANSPORT=audit"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("journalctl args %q, want %q", got, want)
	}
}

// journald can hold the same records (its audit transport): an event in both
// the journal and audit.log counts once.
func TestApparmorRecentViolations_EventInBothSourcesCountsOnce(t *testing.T) {
	line := avcLine(time.Now().Add(-time.Hour).Unix(), 77, "ALLOWED", "stalwart-mail", "/x")
	fakeJournalctl(t, "Oct 19 06:00:00 mx audit[1]: "+line[len("type=AVC "):]+"\n", 0)
	withAuditLogs(t, map[string]string{"audit.log": line})

	n, _, err := apparmorRecentViolations("stalwart-mail", 7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("violations = %d, want 1", n)
	}
}

// Journal-only hosts (no auditd) still count the journal's records.
func TestApparmorRecentViolations_JournalOnly(t *testing.T) {
	line := avcLine(time.Now().Add(-time.Hour).Unix(), 3, "DENIED", "jabali-bulwark", "/x")
	fakeJournalctl(t, "Oct 19 06:00:00 mx kernel: audit: type=1400 "+line[len("type=AVC msg="):]+"\n", 0)
	withAuditLogs(t, nil)

	n, _, err := apparmorRecentViolations("jabali-bulwark", 7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("violations = %d, want 1", n)
	}
}

// A non-positive window must not shrink the scan to nothing: the journal is
// read for the whole boot and every audit log record counts.
func TestApparmorRecentViolations_NonPositiveWindowScansEverything(t *testing.T) {
	argsFile := fakeJournalctl(t, "", 0)
	withAuditLogs(t, map[string]string{
		"audit.log": avcLine(time.Now().Add(-30*24*time.Hour).Unix(), 1, "ALLOWED", "stalwart-mail", "/old"),
	})

	n, _, err := apparmorRecentViolations("stalwart-mail", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("violations = %d, want 1", n)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "-b\n") || strings.Contains(string(args), "--since") {
		t.Errorf("journalctl args %q, want -b and no --since", args)
	}
}

// The gate fails closed: a source it can't read is an error, never a clean
// soak.
func TestApparmorRecentViolations_FailsClosed(t *testing.T) {
	t.Run("journalctl fails", func(t *testing.T) {
		fakeJournalctl(t, "", 1)
		withAuditLogs(t, nil)
		if _, _, err := apparmorRecentViolations("stalwart-mail", 7); err == nil {
			t.Fatal("want an error when journalctl fails")
		}
	})
	t.Run("audit log unreadable", func(t *testing.T) {
		fakeJournalctl(t, "", 0)
		dir := withAuditLogs(t, nil)
		// A directory where a rotation should be: opening works, reading fails.
		if err := os.Mkdir(filepath.Join(dir, "audit.log.3"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := apparmorRecentViolations("stalwart-mail", 7); err == nil {
			t.Fatal("want an error for an unreadable audit log")
		}
	})
	t.Run("audit log can't be opened", func(t *testing.T) {
		fakeJournalctl(t, "", 0)
		dir := withAuditLogs(t, nil)
		// A dangling symlink matches the glob but can't be opened, as root too.
		if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "audit.log.5")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := apparmorRecentViolations("stalwart-mail", 7); err == nil {
			t.Fatal("want an error for an audit log that can't be opened")
		}
	})
	t.Run("corrupt compressed rotation", func(t *testing.T) {
		fakeJournalctl(t, "", 0)
		dir := withAuditLogs(t, nil)
		if err := os.WriteFile(filepath.Join(dir, "audit.log.4.gz"), []byte("not gzip"), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, _, err := apparmorRecentViolations("stalwart-mail", 7); err == nil {
			t.Fatal("want an error for a corrupt .gz rotation")
		}
	})
	t.Run("line too long to scan", func(t *testing.T) {
		fakeJournalctl(t, "", 0)
		withAuditLogs(t, map[string]string{
			"audit.log": strings.Repeat("x", 2<<20) + "\n" + avcLine(time.Now().Unix(), 1, "ALLOWED", "stalwart-mail", "/x"),
		})
		if _, _, err := apparmorRecentViolations("stalwart-mail", 7); err == nil {
			t.Fatal("want an error when a scan is cut short")
		}
	})
}
