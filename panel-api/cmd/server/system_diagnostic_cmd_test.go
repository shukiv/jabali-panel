package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestPrintDiagReport checks the CLI renders `jabali system diagnostic` as a
// readable block (not raw JSON), using the real response shape.
func TestPrintDiagReport(t *testing.T) {
	var buf bytes.Buffer
	printDiagReport(&buf, diagReport{
		URL:            "https://enclosed.jabali-panel.com/01kyk8m8y79w2y0pns2ywkqg75#pw:oCypiWiUFu6kSnaJSN0BeLf93bftBMUv9aelT_HQlgg",
		Password:       "Uh9LoSe2h6zL1aBydukkjMcfirs",
		NoteID:         "01kyk8m8y79w2y0pns2ywkqg75",
		ByteCount:      468480,
		GeneratedAt:    "2026-07-28T02:24:58Z",
		RedactionCount: 0,
		FileCount:      50,
	})
	out := buf.String()

	for _, want := range []string{
		"Diagnostic bundle uploaded",
		"expires in 7 days",
		"50 files",
		"0 redactions",
		"https://enclosed.jabali-panel.com/01kyk8m8y79w2y0pns2ywkqg75#pw:", // full link, key intact
		"Uh9LoSe2h6zL1aBydukkjMcfirs",                                      // password shown
		"2026-07-28 02:24 UTC",                                             // formatted time, not raw RFC3339
		"do NOT paste it into a public issue",                              // privacy warning
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnostic output missing %q\n---\n%s", want, out)
		}
	}
	// Must no longer be raw JSON.
	if strings.Contains(out, `"byte_count"`) || strings.Contains(out, `"note_id"`) {
		t.Errorf("output still looks like raw JSON:\n%s", out)
	}
}

// A malformed generated_at must degrade gracefully (show it as-is, not crash).
func TestPrintDiagReport_BadTime(t *testing.T) {
	var buf bytes.Buffer
	printDiagReport(&buf, diagReport{URL: "https://x/#pw:y", Password: "p", GeneratedAt: "not-a-time"})
	if !strings.Contains(buf.String(), "not-a-time") {
		t.Errorf("bad timestamp should be shown verbatim:\n%s", buf.String())
	}
}

// TestPrintDiagReport_WithClaimCode: when a claim code is present, it leads as
// the primary, public-safe hand-off (GH #357 claim-code).
func TestPrintDiagReport_WithClaimCode(t *testing.T) {
	var buf bytes.Buffer
	printDiagReport(&buf, diagReport{
		URL: "https://enclosed/#pw:k", Password: "pw", NoteID: "n1",
		ByteCount: 1000, GeneratedAt: "2026-07-28T02:24:58Z", FileCount: 3,
		ClaimCode: "JAB-7QX9K2P4",
	})
	out := buf.String()
	if !strings.Contains(out, "JAB-7QX9K2P4") {
		t.Errorf("claim code not shown:\n%s", out)
	}
	if !strings.Contains(out, "safe to share anywhere") {
		t.Errorf("public-safe hint missing:\n%s", out)
	}
}
