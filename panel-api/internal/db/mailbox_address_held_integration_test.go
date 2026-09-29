//go:build integration

// Integration test for repository.MailboxAddressHeld: it must say "held"
// exactly when migration 000306's trigger refuses a mailbox at the address.
// The mailbox create doors ask it before they clear the address on the mail
// server, so a false "held" would refuse a create the database allows, and a
// false "free" would let a refused create take a live alias off its account
// in Stalwart's registry.
//
//	JABALI_TEST_DATABASE_URL=... go test -tags integration ./panel-api/internal/db/ -run MailboxAddressHeld

package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func TestIntegration_MailboxAddressHeld(t *testing.T) {
	dsn := testDSN(t)
	resetSchema(t, dsn)

	gdb, err := db.Open(db.Options{DSN: dsn, Silent: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, _ := gdb.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	ctx := context.Background()
	now := time.Now().UTC()

	uname := "ah" + ids.NewULID()[:10]
	user := &models.User{ID: ids.NewULID(), Username: &uname, Email: uname + "@example.com", PasswordHash: "$2a$12$xxxxxxxxxxxxxxxxxxxxxx"}
	require.NoError(t, repository.NewUserRepository(gdb).Create(ctx, user))
	domains := repository.NewDomainRepository(gdb)
	domA := &models.Domain{ID: ids.NewULID(), UserID: user.ID, Name: "a.example.com", EmailEnabled: true}
	domB := &models.Domain{ID: ids.NewULID(), UserID: user.ID, Name: "b.example.com", EmailEnabled: true}
	require.NoError(t, domains.Create(ctx, domA))
	require.NoError(t, domains.Create(ctx, domB))

	mailboxes := repository.NewMailboxRepository(gdb)
	fwds := repository.NewEmailForwarderRepository(gdb)
	mailbox := func(domainID, local string) *models.Mailbox {
		return &models.Mailbox{ID: ids.NewULID(), DomainID: domainID, LocalPart: local, PasswordHash: "$2a$12$xxxxxxxxxxxxxxxxxxxxxx", QuotaBytes: 1 << 24, CreatedAt: now, UpdatedAt: now}
	}

	owner := mailbox(domA.ID, "owner")
	require.NoError(t, mailboxes.Create(ctx, owner))
	forwarder := func(typ, local string) *models.EmailForwarder {
		lp := local
		return &models.EmailForwarder{ID: ids.NewULID(), MailboxID: &owner.ID, DomainID: domA.ID, Type: typ, LocalPart: &lp, Target: local + "@a.example.com", Enabled: true}
	}
	require.NoError(t, fwds.Create(ctx, forwarder("alias", "sales")))
	paused := forwarder("alias", "paused")
	require.NoError(t, fwds.Create(ctx, paused))
	// GORM writes the column default for a false bool, so turn it off here.
	require.NoError(t, gdb.Exec("UPDATE email_forwarders SET enabled = 0 WHERE id = ?", paused.ID).Error)
	ext := forwarder("external", "ext")
	ext.Target = "someone@elsewhere.example"
	require.NoError(t, fwds.Create(ctx, ext))
	require.NoError(t, repository.NewMailGroupRepository(gdb).Create(ctx, &models.MailGroup{ID: ids.NewULID(), DomainID: domA.ID, LocalPart: "team", GroupKind: "distribution", CreatedAt: now, UpdatedAt: now}))
	desk := "desk"
	require.NoError(t, repository.NewSharedResourceRepository(gdb).Create(ctx, &models.SharedResource{ID: ids.NewULID(), DomainID: domA.ID, Kind: "calendar", LocalPart: &desk, CreatedAt: now, UpdatedAt: now}))

	for _, tc := range []struct {
		name     string
		domainID string
		local    string
		held     bool
	}{
		{"free address", domA.ID, "nobody", false},
		{"enabled alias", domA.ID, "sales", true},
		{"alias in another case", domA.ID, "SALES", true},
		{"disabled alias", domA.ID, "paused", true},
		{"mail group", domA.ID, "team", true},
		{"shared resource", domA.ID, "desk", true},
		{"external forward with a local part", domA.ID, "ext", false},
		{"same local part in another domain", domB.ID, "team", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, err := repository.MailboxAddressHeld(ctx, mailboxes, tc.domainID, tc.local)
			require.NoError(t, err)
			require.Equal(t, tc.held, held)

			err = mailboxes.Create(ctx, mailbox(tc.domainID, tc.local))
			require.Equal(t, held, errors.Is(err, repository.ErrAddressInUse), "check says held=%v, insert err = %v", held, err)
			if !held {
				require.NoError(t, err)
			}
		})
	}
}
