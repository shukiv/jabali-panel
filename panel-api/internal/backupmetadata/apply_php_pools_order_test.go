package backupmetadata

import (
	"context"
	"testing"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a backup now carries every PHP pool of the account, the default
// first. The account's default pool is its earliest-created one, so the
// restored pools keep the backup's order, and each domain comes back on the
// pool of its own version.
func TestApply_RestoresEveryPoolInTheBackupsOrder(t *testing.T) {
	pools := &ppPools{}
	doms := &ppDomains{pools: pools}
	mb := &dcMailboxes{}
	meta := ppMeta()
	p83 := "p-src-83"
	meta.PHPPools = append(meta.PHPPools, internalbackup.MetadataPHPPool{ID: p83, PHPVersion: "8.3", PmMode: "ondemand", PmMaxChildren: 5})
	meta.Domains[0].PHPPoolID = &p83

	r := Apply(context.Background(), meta, ppDeps(pools, doms, mb))

	if len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	if len(pools.rows) != 2 || pools.rows[0].PHPVersion != "8.4" || pools.rows[1].PHPVersion != "8.3" {
		t.Fatalf("pools = %+v, want 8.4 then 8.3", pools.rows)
	}
	if !pools.rows[0].CreatedAt.Before(pools.rows[1].CreatedAt) {
		t.Errorf("the default pool (%s) is not the earliest created: %v vs %v",
			pools.rows[0].ID, pools.rows[0].CreatedAt, pools.rows[1].CreatedAt)
	}
	if len(doms.created) != 1 || doms.created[0].PHPPoolID == nil || *doms.created[0].PHPPoolID != p83 {
		t.Fatalf("domains = %+v, want alice.org on the 8.3 pool", doms.created)
	}
}

// The account created for the restore can already have its default pool,
// made by the reconciler after the restore began: under its own id, or under
// the backup's (a restore run again). A pool restored next to it is still
// created after it, or it would become the account's default.
func TestApply_RestoredPoolIsCreatedAfterTheAccountsDefault(t *testing.T) {
	for _, id := range []string{"p-local", "p-src"} {
		t.Run(id, func(t *testing.T) {
			local := models.PHPPool{ID: id, UserID: "u1", PHPVersion: "8.4", CreatedAt: time.Now().UTC().Add(time.Hour)}
			pools := &ppPools{rows: []models.PHPPool{local}}
			doms := &ppDomains{pools: pools}
			meta := ppMeta()
			p83 := "p-src-83"
			meta.PHPPools = append(meta.PHPPools, internalbackup.MetadataPHPPool{ID: p83, PHPVersion: "8.3", PmMode: "ondemand", PmMaxChildren: 5})
			meta.Domains[0].PHPPoolID = &p83

			r := Apply(context.Background(), meta, ppDeps(pools, doms, &dcMailboxes{}))

			if len(r.Errors) != 0 {
				t.Fatalf("errors: %v", r.Errors)
			}
			if len(pools.rows) != 2 || pools.rows[1].PHPVersion != "8.3" {
				t.Fatalf("pools = %+v, want the account's 8.4 pool and a new 8.3 one", pools.rows)
			}
			if !pools.rows[0].CreatedAt.Before(pools.rows[1].CreatedAt) {
				t.Errorf("the 8.3 pool (%v) is created before the account's default (%v): it would become the default",
					pools.rows[1].CreatedAt, pools.rows[0].CreatedAt)
			}
		})
	}
}
