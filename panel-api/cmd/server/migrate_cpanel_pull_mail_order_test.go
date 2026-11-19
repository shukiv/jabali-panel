package main

import (
	"os"
	"strings"
	"testing"
)

// A domain's mail facet is decided in ImportDomains from the ~/mail/<domain>
// directories (scanMailDomains). On a cPanel/WHM SSH pull those directories
// only exist once cpanelPullMail has copied ~/mail. When the copy ran after
// ImportDomains, every domain of a live pull was created mail-off and every
// mailbox came in send-only ("the domain's mail is hosted elsewhere"), though
// the source received the domain's mail.
//
// The password-hash files stay a separate, late step: pulled after the domains,
// right before ImportMailboxes, so they are not on disk through the docroot
// rsync.
func TestCpanelMailCopyRunsBeforeImportDomains(t *testing.T) {
	src, err := os.ReadFile("migrate_run_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	pullMail := strings.Index(body, "cpanelPullMail(ctx, restoreAgent.Call")
	setRoot := strings.Index(body, "p.parsed.MailRoot = root")
	domains := strings.Index(body, "cpanel.ImportDomains(ctx, domainsRepo, restoreAgent, p.parsed")
	pullShadow := strings.Index(body, "cpanelPullMailShadow(ctx, restoreAgent.Call")
	imp := strings.Index(body, "mailRes, err := cpanel.ImportMailboxes(")
	if pullMail < 0 || setRoot < 0 || domains < 0 || pullShadow < 0 || imp < 0 {
		t.Fatalf("missing wiring: pullMail=%d setRoot=%d domains=%d pullShadow=%d import=%d",
			pullMail, setRoot, domains, pullShadow, imp)
	}
	if !(pullMail < setRoot && setRoot < domains) {
		t.Fatal("~/mail must be copied, and MailRoot set, before ImportDomains decides each domain's mail facet")
	}
	if !(domains < pullShadow && pullShadow < imp) {
		t.Fatal("the password-hash files must be pulled after the domains, right before ImportMailboxes")
	}
	if strings.Count(body, "cpanelPullMail(ctx, restoreAgent.Call") != 1 {
		t.Fatal("~/mail must be copied exactly once")
	}
}
