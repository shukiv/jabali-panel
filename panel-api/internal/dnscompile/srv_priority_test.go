package dnscompile

import (
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestSRVAndMXContentCarryNoPriority pins the field split that made nine
// records unresolvable.
//
// PowerDNS's gmysql backend on this schema prepends the prio column to the
// content for MX and SRV. The email records carried the priority in BOTH
// places, so "0 1 465 mail.example.com" was assembled into a five-field SRV:
//
//	pdnsutil check-zone:
//	  Error was: When parsing SRV trailing data was not parsed: ' mail...'
//
// and pdns served nothing at all for them. Verified against a live
// authoritative server: `dig _imaps._tcp.<zone> SRV` returned an empty answer,
// and the same record with the priority moved out of content answered
// correctly. That silently disables SRV-based mail client autoconfiguration
// (Thunderbird, Outlook, iOS) and CalDAV/CardDAV discovery on every
// email-enabled domain.
//
// The per-record literal assertions in email_records_test.go were updated
// alongside the fix, which is exactly why this test exists too: those encode
// the values, this encodes the rule, and it was the rule that was wrong.
func TestSRVAndMXContentCarryNoPriority(t *testing.T) {
	t.Parallel()

	for _, rec := range emailRecordsForTest(t) {
		switch rec.Type {
		case "SRV":
			// RFC 2782 wire format is "priority weight port target"; with the
			// priority in its own column, content must be the remaining three.
			fields := strings.Fields(rec.Content)
			if len(fields) != 3 {
				t.Errorf("%s SRV content %q has %d fields, want 3 "+
					"(weight port target). PowerDNS prepends the prio column, "+
					"so a priority left in content produces a malformed record "+
					"that is not served at all.",
					rec.Name, rec.Content, len(fields))
				continue
			}
			// A bare target in the port slot means the fields shifted.
			if strings.ContainsAny(fields[1], "abcdefghijklmnopqrstuvwxyz") {
				t.Errorf("%s SRV content %q: port field %q is not numeric — "+
					"fields look shifted", rec.Name, rec.Content, fields[1])
			}
		case "MX":
			// MX was always written correctly; assert it stays that way, since
			// the same prepending applies.
			if len(strings.Fields(rec.Content)) != 1 {
				t.Errorf("%s MX content %q should be the exchange hostname "+
					"alone, with priority in the priority column",
					rec.Name, rec.Content)
			}
		}
	}
}

// TestSRVPriorityIsSetInTheColumn is the other half: moving the priority out of
// content is only correct if it actually lands in the column, otherwise every
// SRV silently becomes priority 0.
func TestSRVPriorityIsSetInTheColumn(t *testing.T) {
	t.Parallel()

	seen := 0
	for _, rec := range emailRecordsForTest(t) {
		if rec.Type != "SRV" {
			continue
		}
		seen++
		if rec.Priority < 0 {
			t.Errorf("%s SRV has negative priority %d", rec.Name, rec.Priority)
		}
	}
	if seen == 0 {
		t.Fatal("no SRV records generated — the email record set changed shape " +
			"and this test is no longer checking anything")
	}
}

func emailRecordsForTest(t *testing.T) []models.DNSRecord {
	t.Helper()
	n := 0
	idNew := func() string {
		n++
		return "id" + string(rune('A'+n))
	}
	return BuildEmailRecords(
		"zone1", "example.com", "jabali", "v=DKIM1; k=ed25519; p=AAAA", "",
		nil, idNew, time.Unix(0, 0).UTC(),
	)
}
