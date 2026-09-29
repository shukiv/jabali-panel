package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestInstallNeverDropsPostmasterMail guards Stalwart's
// ReportSettings.inboundReportForwarding (ADR-0110). Stalwart reads the DMARC,
// TLS and ARF reports sent to postmaster@* and the panel imports them. It is
// tempting to turn forwarding off so the reports stop landing in the
// postmaster mailbox, but on Stalwart 0.16 that drops ALL mail to postmaster@,
// human mail included, without a bounce (verified on the .60 test box,
// 2026-09-29). Neither install path may set it false.
func TestInstallNeverDropsPostmasterMail(t *testing.T) {
	root := repoRootT(t)
	for _, rel := range []string{"install.sh", filepath.Join("install", "stalwart", "apply-plan.json.tmpl")} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if reportForwardingOffRe.Match(b) {
			t.Errorf("%s sets inboundReportForwarding false: Stalwart would drop all mail to postmaster@", rel)
		}
	}
}

var reportForwardingOffRe = regexp.MustCompile(`"?inboundReportForwarding"?\s*[:=]\s*false`)
