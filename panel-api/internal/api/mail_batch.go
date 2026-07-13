package api

import (
	"context"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-147: mail list handlers (forwarders / shares / groups) resolved each row
// with per-row FindByID for the owning mailbox + domain — an N+1 that issues
// hundreds of queries for a full page. These helpers batch-load by id in one
// query each and return an id→row map for O(1) resolution.

func dedupeIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// mailboxMapByID batch-loads the given mailbox ids into an id→*Mailbox map.
// A load error yields an empty map (callers skip rows they can't resolve, the
// same failure mode as the per-row FindByID it replaced).
func mailboxMapByID(ctx context.Context, repo repository.MailboxRepository, ids []string) map[string]*models.Mailbox {
	m := map[string]*models.Mailbox{}
	rows, err := repo.FindByIDs(ctx, dedupeIDs(ids))
	if err != nil {
		return m
	}
	for i := range rows {
		m[rows[i].ID] = &rows[i]
	}
	return m
}

// domainMapByID batch-loads the given domain ids into an id→*Domain map.
func domainMapByID(ctx context.Context, repo repository.DomainRepository, ids []string) map[string]*models.Domain {
	m := map[string]*models.Domain{}
	rows, err := repo.FindByIDs(ctx, dedupeIDs(ids))
	if err != nil {
		return m
	}
	for i := range rows {
		m[rows[i].ID] = &rows[i]
	}
	return m
}
