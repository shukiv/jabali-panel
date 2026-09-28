package backupmetadata

import (
	"context"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1637: a backup is input. A mailbox at the domain directory's address,
// in any spelling, is not restored: it would share its Stalwart principal
// with the directory's host.
func TestApply_RefusesTheDirectoryAddress(t *testing.T) {
	for _, local := range []string{"jabali-directory", "Jabali-Directory", "jabali-directory+x"} {
		t.Run(local, func(t *testing.T) {
			_, mb, _, deps := dcDeps()
			deps.CheckDomain = func(context.Context, *models.Domain, string) ([]string, error) { return nil, nil }
			meta := dcMeta()
			meta.Domains = meta.Domains[:1]
			meta.Domains[0].Mailboxes = append(meta.Domains[0].Mailboxes, internalbackup.MetadataMailbox{ID: "mb-dir", LocalPart: local})

			r := Apply(context.Background(), meta, deps)

			if mb.created != 1 {
				t.Fatalf("mailboxes created = %d, want only info", mb.created)
			}
			if !hasError(r.Errors, "mailbox mb-dir: not restored") {
				t.Fatalf("refusal not reported: %v", r.Errors)
			}
		})
	}
}
