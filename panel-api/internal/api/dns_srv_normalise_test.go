package api

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func intPtr(i int) *int { return &i }

// TestNormaliseSRVRecord covers the user-facing half of the SRV priority bug.
//
// PowerDNS prepends the priority column to SRV content, so a record carrying
// the priority in both places assembles into five fields and is not served at
// all — the record simply does not resolve. The panel's validator used to
// REQUIRE the four-field form, which meant every SRV created through the UI or
// API was broken, and a caller supplying the correct three fields was rejected.
//
// JAB-28 fixed this same bug in the cPanel BIND importer. The panel's own
// create/update path was missed.
//
// Every DNS tutorial and every zone file writes the full RFC 2782 string, so
// the fix accepts it and splits the priority out rather than rejecting it.
func TestNormaliseSRVRecord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		in          models.DNSRecord
		explicit    *int
		wantContent string
		wantPrio    int
	}{
		{
			name:        "full RFC form is split",
			in:          models.DNSRecord{Type: "SRV", Content: "10 1 465 mail.example.com"},
			wantContent: "1 465 mail.example.com",
			wantPrio:    10,
		},
		{
			name:        "already-correct three fields are untouched",
			in:          models.DNSRecord{Type: "SRV", Content: "1 465 mail.example.com", Priority: 10},
			wantContent: "1 465 mail.example.com",
			wantPrio:    10,
		},
		{
			// The caller being explicit is the caller being unambiguous.
			name:        "explicit priority wins over the inline one",
			in:          models.DNSRecord{Type: "SRV", Content: "10 1 465 mail.example.com", Priority: 20},
			explicit:    intPtr(20),
			wantContent: "1 465 mail.example.com",
			wantPrio:    20,
		},
		{
			name:        "priority zero is preserved, not treated as unset",
			in:          models.DNSRecord{Type: "SRV", Content: "0 1 465 mail.example.com"},
			wantContent: "1 465 mail.example.com",
			wantPrio:    0,
		},
		{
			// Left alone so ValidateDNSRecord reports the real problem rather
			// than this silently mangling it first.
			name:        "non-numeric leading field is left for the validator",
			in:          models.DNSRecord{Type: "SRV", Content: "abc 1 465 mail.example.com"},
			wantContent: "abc 1 465 mail.example.com",
			wantPrio:    0,
		},
		{
			name:        "out-of-range priority is left for the validator",
			in:          models.DNSRecord{Type: "SRV", Content: "70000 1 465 mail.example.com"},
			wantContent: "70000 1 465 mail.example.com",
			wantPrio:    0,
		},
		{
			name:        "non-SRV types are never touched",
			in:          models.DNSRecord{Type: "TXT", Content: "10 1 465 mail.example.com"},
			wantContent: "10 1 465 mail.example.com",
			wantPrio:    0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.in
			NormaliseSRVRecord(&rec, tc.explicit)
			if rec.Content != tc.wantContent {
				t.Errorf("content = %q, want %q", rec.Content, tc.wantContent)
			}
			if rec.Priority != tc.wantPrio {
				t.Errorf("priority = %d, want %d", rec.Priority, tc.wantPrio)
			}
		})
	}
}

// TestValidateDNSRecord_SRVShape pins that the validator now wants the stored
// shape. Accepting four fields here would let the doubled priority back
// through for any caller that skips normalisation.
func TestValidateDNSRecord_SRVShape(t *testing.T) {
	t.Parallel()

	ok := &models.DNSRecord{Type: "SRV", Name: "_imaps._tcp", Content: "1 993 mail.example.com", Priority: 0}
	if err := ValidateDNSRecord(ok); err != nil {
		t.Errorf("three-field SRV content should validate, got: %v", err)
	}

	doubled := &models.DNSRecord{Type: "SRV", Name: "_imaps._tcp", Content: "0 1 993 mail.example.com"}
	if err := ValidateDNSRecord(doubled); err == nil {
		t.Error("four-field SRV content must be rejected at validation — " +
			"PowerDNS prepends the priority column, so storing it would " +
			"produce a record that never resolves")
	}

	badPort := &models.DNSRecord{Type: "SRV", Name: "_imaps._tcp", Content: "1 0 mail.example.com"}
	if err := ValidateDNSRecord(badPort); err == nil {
		t.Error("port 0 should be rejected")
	}
}
