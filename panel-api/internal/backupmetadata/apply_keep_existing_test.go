package backupmetadata

import (
	"context"
	"errors"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: with "Overwrite existing items with the backup" off
// (KeepExisting), a mailbox the account already has keeps its autoresponder.
// One without an autoresponder, or a mailbox the restore creates, gets the
// backup's.
func TestApply_KeepingAnExistingMailboxKeepsItsAutoresponder(t *testing.T) {
	for _, c := range []struct {
		name                 string
		keep, mailboxThere   bool
		replyThere           bool
		findErr              error
		wantWritten, wantErr bool
	}{
		{name: "keep: mailbox and reply there", keep: true, mailboxThere: true, replyThere: true},
		{name: "keep: mailbox there, no reply", keep: true, mailboxThere: true, wantWritten: true},
		{name: "keep: new mailbox", keep: true, wantWritten: true},
		// A reply row left behind under the id of a mailbox the restore
		// creates is not the account's: the new mailbox gets the backup's.
		{name: "keep: new mailbox, stale reply row", keep: true, replyThere: true, wantWritten: true},
		{name: "keep: lookup fails", keep: true, mailboxThere: true, findErr: errors.New("db down"), wantErr: true},
		{name: "overwrite: mailbox and reply there", mailboxThere: true, replyThere: true, wantWritten: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newBnFixture()
			if c.mailboxThere {
				f.mbs.existing = []models.Mailbox{{ID: "mb-here", DomainID: "d-here", LocalPart: "info", EmailCached: "info@alice.org"}}
			}
			f.ars.existing = map[string]bool{"mb-here": c.replyThere}
			f.ars.findErr = c.findErr
			m := owMeta()
			away := "away"
			dm := aliceDomain("d-here")
			dm.Mailboxes = []internalbackup.MetadataMailbox{{ID: "mb-here", LocalPart: "info", EmailCached: "info@alice.org",
				Autoresponder: &internalbackup.MetadataAutoresponder{Enabled: true, Subject: &away}}}
			m.Domains = []internalbackup.MetadataDomain{dm}
			d := f.deps()
			d.KeepExisting = c.keep

			r := Apply(context.Background(), m, d)

			if written := len(f.ars.updated) == 1; written != c.wantWritten {
				t.Errorf("autoresponder written = %v (%v), want %v (errors %v)", written, f.ars.updated, c.wantWritten, r.Errors)
			}
			if got := strings.Contains(strings.Join(r.Errors, "|"), "autoresponder mb-here: not restored: lookup: db down"); got != c.wantErr {
				t.Errorf("errors %v, want the failed lookup reported = %v", r.Errors, c.wantErr)
			}
		})
	}
}
