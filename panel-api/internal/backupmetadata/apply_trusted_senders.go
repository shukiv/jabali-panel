package backupmetadata

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/trustedsenders"
)

// restoreTrustedSenders adds the backup's trusted senders (GH #2017) to the
// restored mailbox mbID; metaID is its id in the backup. Each address is
// checked as the API checks it, since the archive may be an uploaded file,
// and the mailbox's cap holds. A sender the mailbox already trusts is
// skipped. The list is added to, never replaced.
func restoreTrustedSenders(ctx context.Context, d Deps, r *ApplyResult, mbID, metaID string, senders []string) {
	n, err := d.TrustedSenders.CountByMailbox(ctx, mbID)
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: trusted senders not restored: count: %v", metaID, err))
		return
	}
	for _, raw := range senders {
		addr, err := mailaddr.CanonicaliseSender(strings.TrimSpace(raw))
		if err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: trusted sender %q: not restored: %v", metaID, raw, err))
			continue
		}
		if n >= trustedsenders.MaxPerMailbox {
			r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: trusted sender %q: not restored: a mailbox trusts at most %d senders", metaID, raw, trustedsenders.MaxPerMailbox))
			continue
		}
		row := &models.MailboxTrustedSender{ID: ids.NewULID(), MailboxID: mbID, Address: addr}
		switch err := d.TrustedSenders.Create(ctx, row); {
		case errors.Is(err, repository.ErrConflict):
			r.Skipped++
		case err != nil:
			r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: trusted sender %q: create: %v", metaID, raw, err))
		default:
			n++
		}
	}
}
