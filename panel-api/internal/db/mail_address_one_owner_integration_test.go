//go:build integration

// Integration test for migration 000306: a mailbox never shares its address
// with an alias, a mail group or a shared resource in the same domain.
// Stalwart resolves an address in its registry first and keeps every alias
// it has seen, so a mailbox at an alias's address signed in to the alias
// owner's account.
//
//	JABALI_TEST_DATABASE_URL=... go test -tags integration ./panel-api/internal/db/ -run MailAddressOneOwner

package db_test

import (
	_ "embed"

	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

//go:embed migrations/000306_mail_address_one_owner.up.sql
var oneOwnerUpSQL string

func TestIntegration_MailAddressOneOwner(t *testing.T) {
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

	uname := "ao" + ids.NewULID()[:10]
	user := &models.User{ID: ids.NewULID(), Username: &uname, Email: uname + "@example.com", PasswordHash: "$2a$12$xxxxxxxxxxxxxxxxxxxxxx"}
	require.NoError(t, repository.NewUserRepository(gdb).Create(ctx, user))
	domains := repository.NewDomainRepository(gdb)
	domA := &models.Domain{ID: ids.NewULID(), UserID: user.ID, Name: "a.example.com", EmailEnabled: true}
	domB := &models.Domain{ID: ids.NewULID(), UserID: user.ID, Name: "b.example.com", EmailEnabled: true}
	require.NoError(t, domains.Create(ctx, domA))
	require.NoError(t, domains.Create(ctx, domB))

	mailboxes := repository.NewMailboxRepository(gdb)
	fwds := repository.NewEmailForwarderRepository(gdb)
	groups := repository.NewMailGroupRepository(gdb)
	resources := repository.NewSharedResourceRepository(gdb)
	owners := repository.NewMailAddressOwnerRepository(gdb)

	mailbox := func(domainID, local string) *models.Mailbox {
		return &models.Mailbox{ID: ids.NewULID(), DomainID: domainID, LocalPart: local, PasswordHash: "$2a$12$xxxxxxxxxxxxxxxxxxxxxx", QuotaBytes: 1 << 24, CreatedAt: now, UpdatedAt: now}
	}
	alias := func(mb *models.Mailbox, local string, enabled bool) *models.EmailForwarder {
		lp := local
		return &models.EmailForwarder{ID: ids.NewULID(), MailboxID: &mb.ID, DomainID: mb.DomainID, Type: "alias", LocalPart: &lp, Target: local + "@a.example.com", Enabled: enabled}
	}
	group := func(domainID, local string) *models.MailGroup {
		return &models.MailGroup{ID: ids.NewULID(), DomainID: domainID, LocalPart: local, GroupKind: "distribution", CreatedAt: now, UpdatedAt: now}
	}
	resource := func(domainID, local string) *models.SharedResource {
		lp := local
		return &models.SharedResource{ID: ids.NewULID(), DomainID: domainID, Kind: "calendar", LocalPart: &lp, CreatedAt: now, UpdatedAt: now}
	}
	refused := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		require.True(t, errors.Is(err, repository.ErrAddressInUse), "want ErrAddressInUse, got %v", err)
	}
	const triggerMsg = "the address already belongs to an alias, group or mailbox"

	ceo := mailbox(domA.ID, "ceo")
	require.NoError(t, mailboxes.Create(ctx, ceo))
	require.NoError(t, fwds.Create(ctx, alias(ceo, "sales", true)))
	require.NoError(t, fwds.Create(ctx, alias(ceo, "paused", false)))
	// GORM's default:1 tag stores Enabled=false as true on Create; set it.
	require.NoError(t, gdb.Exec("UPDATE email_forwarders SET enabled = 0 WHERE domain_id = ? AND local_part = 'paused'", domA.ID).Error)
	require.NoError(t, groups.Create(ctx, group(domA.ID, "team")))
	require.NoError(t, resources.Create(ctx, resource(domA.ID, "room")))

	t.Run("a mailbox at an alias, group or resource address is refused", func(t *testing.T) {
		refused(t, mailboxes.Create(ctx, mailbox(domA.ID, "sales")))
		refused(t, mailboxes.Create(ctx, mailbox(domA.ID, "SALES")))
		refused(t, mailboxes.Create(ctx, mailbox(domA.ID, "paused"))) // a disabled alias counts
		refused(t, mailboxes.Create(ctx, mailbox(domA.ID, "team")))
		refused(t, mailboxes.Create(ctx, mailbox(domA.ID, "room")))
		var n int64
		require.NoError(t, gdb.Table("mailboxes").Where("domain_id = ? AND local_part IN ('sales','paused','team','room')", domA.ID).Count(&n).Error)
		require.Zero(t, n)
		// The same local part in another domain is someone else's address.
		require.NoError(t, mailboxes.Create(ctx, mailbox(domB.ID, "sales")))
	})

	t.Run("an alias, group or resource at a mailbox address is refused", func(t *testing.T) {
		info := mailbox(domA.ID, "info")
		require.NoError(t, mailboxes.Create(ctx, info))
		refused(t, fwds.Create(ctx, alias(ceo, "info", true)))
		refused(t, fwds.Create(ctx, alias(ceo, "INFO", false)))
		refused(t, groups.Create(ctx, group(domA.ID, "info")))
		refused(t, resources.Create(ctx, resource(domA.ID, "info")))
		// A mailbox's own external forward has no address of its own.
		require.NoError(t, fwds.Create(ctx, &models.EmailForwarder{ID: ids.NewULID(), MailboxID: &info.ID, DomainID: domA.ID, Type: "external", Target: "info@elsewhere.example", Enabled: true}))
	})

	t.Run("a row cannot be moved onto a taken address", func(t *testing.T) {
		bob := mailbox(domA.ID, "bob")
		require.NoError(t, mailboxes.Create(ctx, bob))
		require.ErrorContains(t, gdb.Exec("UPDATE mailboxes SET local_part = 'sales' WHERE id = ?", bob.ID).Error, triggerMsg)
		onB := mailbox(domB.ID, "team")
		require.NoError(t, mailboxes.Create(ctx, onB))
		require.ErrorContains(t, gdb.Exec("UPDATE mailboxes SET domain_id = ? WHERE id = ?", domA.ID, onB.ID).Error, triggerMsg)

		spare := alias(ceo, "spare", true)
		require.NoError(t, fwds.Create(ctx, spare))
		bobLocal := "bob"
		spare.LocalPart = &bobLocal
		refused(t, fwds.Update(ctx, spare))
		require.ErrorContains(t, gdb.Exec("UPDATE mail_groups SET local_part = 'bob' WHERE domain_id = ? AND local_part = 'team'", domA.ID).Error, triggerMsg)
		require.ErrorContains(t, gdb.Exec("UPDATE shared_resources SET local_part = 'bob' WHERE domain_id = ? AND local_part = 'room'", domA.ID).Error, triggerMsg)
	})

	// A pair made before 000306 must not have its rows locked. Model one by
	// dropping the mailbox insert trigger for one insert.
	t.Run("an existing pair keeps working", func(t *testing.T) {
		require.NoError(t, gdb.Exec("DROP TRIGGER trg_mailboxes_one_owner_insert").Error)
		legacy := mailbox(domA.ID, "sales")
		err := mailboxes.Create(ctx, legacy)
		recreateMailboxInsertTrigger(t, gdb)
		require.NoError(t, err)

		require.NoError(t, mailboxes.UpdatePasswordHash(ctx, legacy.ID, "$2a$12$yyyyyyyyyyyyyyyyyyyyyy"))
		require.NoError(t, mailboxes.UpdateQuota(ctx, legacy.ID, 1<<25))
		require.NoError(t, gdb.Exec("UPDATE email_forwarders SET enabled = 0 WHERE domain_id = ? AND local_part = 'sales'", domA.ID).Error)
		require.NoError(t, gdb.Exec("UPDATE email_forwarders SET enabled = 1 WHERE domain_id = ? AND local_part = 'sales'", domA.ID).Error)
		// The re-created trigger still refuses.
		refused(t, mailboxes.Create(ctx, mailbox(domA.ID, "team")))
	})

	t.Run("renaming a domain still renames its rows", func(t *testing.T) {
		// The domains AFTER UPDATE triggers UPDATE mailboxes and mail_groups,
		// which runs the 000306 BEFORE UPDATE triggers nested.
		require.NoError(t, gdb.Exec("UPDATE domains SET name = 'renamed.example.com' WHERE id = ?", domA.ID).Error)
		var emails []string
		require.NoError(t, gdb.Raw("SELECT email_cached FROM mailboxes WHERE id = ? UNION ALL SELECT email_cached FROM mail_groups WHERE domain_id = ? AND local_part = 'team'", ceo.ID, domA.ID).Scan(&emails).Error)
		require.Equal(t, []string{"ceo@renamed.example.com", "team@renamed.example.com"}, emails)
		require.NoError(t, gdb.Exec("UPDATE domains SET name = 'a.example.com' WHERE id = ?", domA.ID).Error)
	})

	t.Run("the owner of an address", func(t *testing.T) {
		for addr, want := range map[string]string{
			"ceo@a.example.com":    "ceo@a.example.com",   // a mailbox
			"Spare@A.example.com":  "ceo@a.example.com",   // an alias, any case
			"paused@a.example.com": "",                    // a disabled alias delivers nowhere
			"team@a.example.com":   "team@a.example.com",  // a mail group
			"sales@a.example.com":  "sales@a.example.com", // mailbox and alias: the mailbox wins
			"nobody@a.example.com": "",
			"sales@b.example.com":  "sales@b.example.com",
		} {
			got, err := owners.Owner(ctx, addr)
			require.NoError(t, err)
			require.Equal(t, want, got, "owner of %s", addr)
		}
	})
}

// recreateMailboxInsertTrigger re-creates trg_mailboxes_one_owner_insert from
// the shipped migration, so the test cannot drift from it.
func recreateMailboxInsertTrigger(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	stmt := regexp.MustCompile(`(?s)CREATE TRIGGER trg_mailboxes_one_owner_insert.*?\n  END;`).FindString(oneOwnerUpSQL)
	require.NotEmpty(t, stmt, "trg_mailboxes_one_owner_insert not found in the migration")
	require.NoError(t, gdb.Exec(stmt).Error)
}
