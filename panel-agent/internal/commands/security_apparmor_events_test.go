package commands

import (
	"strings"
	"testing"
	"time"
)

// Real-shape auditd AVC lines (source: production audit.log). The parser must
// read these — with auditd running, AppArmor events land in audit.log and
// never reach `journalctl -k` (the bug this file guards against regressing).
const auditLogSample = `type=AVC msg=audit(1785547714.640:276519): apparmor="DENIED" operation="open" class="file" profile="jabali-fpm-app" name="/home/hlpcoll/logs/php.error.log" pid=3076079 comm="php-fpm8.4" requested_mask="w" denied_mask="w" fsuid=5012 ouid=5012
type=AVC msg=audit(1785547775.636:276624): apparmor="DENIED" operation="open" class="file" profile="stalwart-mail" name="/proc/1052443/smaps" pid=1052443 comm="tokio-rt-worker" requested_mask="r" denied_mask="r" fsuid=990 ouid=990
type=USER_LOGIN msg=audit(1785547800.000:276700): pid=9 uid=0 auid=0 ses=1 res=success
type=AVC msg=audit(1785547836.936:276780): apparmor="ALLOWED" operation="open" class="file" profile="jabali-bulwark" name="/etc/hosts" pid=42 comm="node" requested_mask="r" denied_mask="r" fsuid=991 ouid=0
`

func TestParseApparmorEventLinesDenied(t *testing.T) {
	since := time.Unix(1785500000, 0)
	got := parseApparmorEventLines(strings.NewReader(auditLogSample), "DENIED", since, 50)
	if len(got) != 2 {
		t.Fatalf("want 2 DENIED rows, got %d: %+v", len(got), got)
	}
	// Newest first.
	if got[0].Profile != "stalwart-mail" || got[1].Profile != "jabali-fpm-app" {
		t.Fatalf("want newest-first [stalwart-mail jabali-fpm-app], got [%s %s]", got[0].Profile, got[1].Profile)
	}
	fpm := got[1]
	if fpm.Path != "/home/hlpcoll/logs/php.error.log" {
		t.Errorf("path: got %q", fpm.Path)
	}
	if fpm.Operation != "open" || fpm.RequestedMask != "w" || fpm.DeniedMask != "w" {
		t.Errorf("fields: %+v", fpm)
	}
	if fpm.Comm != "php-fpm8.4" || fpm.PID != "3076079" || fpm.FSUID != "5012" {
		t.Errorf("comm/pid/fsuid: %+v", fpm)
	}
	if fpm.Timestamp != time.Unix(1785547714, 0).UTC().Format(time.RFC3339) {
		t.Errorf("timestamp: got %q", fpm.Timestamp)
	}
}

func TestParseApparmorEventLinesAllowedOnly(t *testing.T) {
	got := parseApparmorEventLines(strings.NewReader(auditLogSample), "ALLOWED", time.Unix(0, 0), 50)
	if len(got) != 1 || got[0].Profile != "jabali-bulwark" {
		t.Fatalf("want 1 ALLOWED jabali-bulwark row, got %+v", got)
	}
}

func TestParseApparmorEventLinesQuotedPathWithSpaces(t *testing.T) {
	line := `type=AVC msg=audit(1785547900.000:1): apparmor="DENIED" operation="open" profile="jabali-fpm-app" name="/home/u/domains/d/public_html/wp content/My File.php" pid=7 comm="php-fpm8.4" requested_mask="r" denied_mask="r"` + "\n"
	got := parseApparmorEventLines(strings.NewReader(line), "DENIED", time.Unix(0, 0), 50)
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].Path != "/home/u/domains/d/public_html/wp content/My File.php" {
		t.Errorf("quoted path with spaces truncated: %q", got[0].Path)
	}
}

func TestParseApparmorEventLinesWindowFilter(t *testing.T) {
	since := time.Unix(1785547800, 0) // between the two DENIED samples
	got := parseApparmorEventLines(strings.NewReader(auditLogSample), "DENIED", since, 50)
	if len(got) != 0 {
		t.Fatalf("both DENIED rows predate since; got %+v", got)
	}
}

func TestParseApparmorEventLinesKeepsNewestOverCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 120; i++ {
		// Monotonic timestamps so "newest" is well-defined.
		b.WriteString(`type=AVC msg=audit(17855` + string(rune('0'+i%10)) + `0000.000:1): apparmor="DENIED" operation="open" profile="p" name="/f" pid=1 comm="c"` + "\n")
	}
	// A distinguishable final (newest) line.
	b.WriteString(`type=AVC msg=audit(1785599999.000:2): apparmor="DENIED" operation="open" profile="newest" name="/last" pid=2 comm="c"` + "\n")
	got := parseApparmorEventLines(strings.NewReader(b.String()), "DENIED", time.Unix(0, 0), 50)
	if len(got) != 50 {
		t.Fatalf("cap: want 50, got %d", len(got))
	}
	if got[0].Profile != "newest" {
		t.Fatalf("newest row must survive the cap and sort first; got %+v", got[0])
	}
}
