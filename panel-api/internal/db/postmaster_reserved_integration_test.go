//go:build integration

// Integration test for migration 000309 (ADR-0110): postmaster@ on every
// domain but the panel hostname's belongs to the server administrator. Stalwart
// keeps postmaster@<domain> on the admin's postmaster account once it has
// delivered there, so a tenant mailbox created later at the address signs in
// to the ADMIN's account. The triggers refuse the row at every door.
//
//	JABALI_TEST_DATABASE_URL=... go test -tags integration ./panel-api/internal/db/ -run PostmasterReserved

package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func TestIntegration_PostmasterReserved(t *testing.T) {
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

	uname := "pm" + ids.NewULID()[:10]
	user := &models.User{ID: ids.NewULID(), Username: &uname, Email: uname + "@example.com", PasswordHash: "$2a$12$xxxxxxxxxxxxxxxxxxxxxx"}
	require.NoError(t, repository.NewUserRepository(gdb).Create(ctx, user))

	domains := repository.NewDomainRepository(gdb)
	panelDom := &models.Domain{ID: ids.NewULID(), UserID: user.ID, Name: "panel.example.com", EmailEnabled: true, IsPanelPrimary: true}
	tenantDom := &models.Domain{ID: ids.NewULID(), UserID: user.ID, Name: "tenant.example.com", EmailEnabled: true}
	otherDom := &models.Domain{ID: ids.NewULID(), UserID: user.ID, Name: "other.example.com", EmailEnabled: true}
	for _, d := range []*models.Domain{panelDom, tenantDom, otherDom} {
		require.NoError(t, domains.Create(ctx, d))
	}

	mailboxes := repository.NewMailboxRepository(gdb)
	mailbox := func(domainID, local string) *models.Mailbox {
		return &models.Mailbox{ID: ids.NewULID(), DomainID: domainID, LocalPart: local, PasswordHash: "$2a$12$xxxxxxxxxxxxxxxxxxxxxx", QuotaBytes: 1 << 24, CreatedAt: now, UpdatedAt: now}
	}
	refused := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		require.True(t, errors.Is(err, mailaddr.ErrPostmasterReserved), "want ErrPostmasterReserved, got %v", err)
	}
	count := func(t *testing.T, table, domainID string) int64 {
		t.Helper()
		var n int64
		require.NoError(t, gdb.Table(table).Where("domain_id = ? AND local_part = 'postmaster'", domainID).Count(&n).Error)
		return n
	}

	t.Run("mailbox on a tenant domain is refused in any case", func(t *testing.T) {
		refused(t, mailboxes.Create(ctx, mailbox(tenantDom.ID, "postmaster")))
		refused(t, mailboxes.Create(ctx, mailbox(tenantDom.ID, "PostMaster")))
		require.Zero(t, count(t, "mailboxes", tenantDom.ID))
		require.NoError(t, mailboxes.Create(ctx, mailbox(tenantDom.ID, "alice")))
	})

	var panelPostmaster *models.Mailbox
	t.Run("mailbox on the panel domain is the admin's", func(t *testing.T) {
		panelPostmaster = mailbox(panelDom.ID, "postmaster")
		require.NoError(t, mailboxes.Create(ctx, panelPostmaster))
	})

	t.Run("a row cannot be moved onto postmaster@", func(t *testing.T) {
		bob := mailbox(tenantDom.ID, "bob")
		require.NoError(t, mailboxes.Create(ctx, bob))
		err := gdb.Exec("UPDATE mailboxes SET local_part = 'postmaster' WHERE id = ?", bob.ID).Error
		require.ErrorContains(t, err, "postmaster@ belongs to the server administrator")
		err = gdb.Exec("UPDATE mailboxes SET domain_id = ? WHERE id = ?", tenantDom.ID, panelPostmaster.ID).Error
		require.ErrorContains(t, err, "postmaster@ belongs to the server administrator")
	})

	t.Run("an existing postmaster row keeps working", func(t *testing.T) {
		// The panel hostname moves: the old admin postmaster row now sits on a
		// domain that is not the panel's, like a tenant postmaster made before
		// 000309. Updates that do not move it still succeed.
		require.NoError(t, gdb.Exec("UPDATE domains SET is_panel_primary = 0 WHERE id = ?", panelDom.ID).Error)
		require.NoError(t, gdb.Exec("UPDATE domains SET is_panel_primary = 1 WHERE id = ?", otherDom.ID).Error)
		require.NoError(t, mailboxes.UpdatePasswordHash(ctx, panelPostmaster.ID, "$2a$12$yyyyyyyyyyyyyyyyyyyyyy"))
		require.NoError(t, mailboxes.UpdateQuota(ctx, panelPostmaster.ID, 1<<25))
		require.NoError(t, gdb.Exec("UPDATE domains SET is_panel_primary = 0 WHERE id = ?", otherDom.ID).Error)
		require.NoError(t, gdb.Exec("UPDATE domains SET is_panel_primary = 1 WHERE id = ?", panelDom.ID).Error)
	})

	t.Run("renaming a domain still renames its existing postmaster rows", func(t *testing.T) {
		// Renaming a domain resyncs email_cached through the AFTER UPDATE
		// triggers on domains, which UPDATE mailboxes and mail_groups and so
		// run the 000309 BEFORE UPDATE triggers nested. Rows made before
		// 000309 sit on a domain that is not the panel's; model them by
		// making them on the panel domain and then moving the panel flag.
		// The group gets a domain of its own: a group at a mailbox's address
		// is refused (000306).
		groupDom := &models.Domain{ID: ids.NewULID(), UserID: user.ID, Name: "groups.example.com", EmailEnabled: true}
		require.NoError(t, domains.Create(ctx, groupDom))
		require.NoError(t, gdb.Exec("UPDATE domains SET is_panel_primary = 0 WHERE id = ?", panelDom.ID).Error)
		require.NoError(t, gdb.Exec("UPDATE domains SET is_panel_primary = 1 WHERE id = ?", groupDom.ID).Error)
		groups := repository.NewMailGroupRepository(gdb)
		group := &models.MailGroup{ID: ids.NewULID(), DomainID: groupDom.ID, LocalPart: "postmaster", GroupKind: "distribution", CreatedAt: now, UpdatedAt: now}
		require.NoError(t, groups.Create(ctx, group))
		require.NoError(t, gdb.Exec("UPDATE domains SET is_panel_primary = 0 WHERE id = ?", groupDom.ID).Error)
		require.NoError(t, gdb.Exec("UPDATE domains SET is_panel_primary = 1 WHERE id = ?", otherDom.ID).Error)

		require.NoError(t, gdb.Exec("UPDATE domains SET name = 'renamed.example.com' WHERE id = ?", panelDom.ID).Error)
		require.NoError(t, gdb.Exec("UPDATE domains SET name = 'renamed-groups.example.com' WHERE id = ?", groupDom.ID).Error)
		var emails []string
		require.NoError(t, gdb.Raw("SELECT email_cached FROM mailboxes WHERE id = ? UNION ALL SELECT email_cached FROM mail_groups WHERE id = ?", panelPostmaster.ID, group.ID).Scan(&emails).Error)
		require.Equal(t, []string{"postmaster@renamed.example.com", "postmaster@renamed-groups.example.com"}, emails)

		require.NoError(t, gdb.Exec("UPDATE domains SET name = 'panel.example.com' WHERE id = ?", panelDom.ID).Error)
		require.NoError(t, gdb.Exec("DELETE FROM mail_groups WHERE id = ?", group.ID).Error)
		require.NoError(t, gdb.Exec("UPDATE domains SET is_panel_primary = 0 WHERE id = ?", otherDom.ID).Error)
		require.NoError(t, gdb.Exec("UPDATE domains SET is_panel_primary = 1 WHERE id = ?", panelDom.ID).Error)
	})

	t.Run("alias, external forward, group and shared resource are refused", func(t *testing.T) {
		alice, err := mailboxes.FindByEmail(ctx, "alice@tenant.example.com")
		require.NoError(t, err)
		fwds := repository.NewEmailForwarderRepository(gdb)
		lp := "postmaster"
		refused(t, fwds.Create(ctx, &models.EmailForwarder{ID: ids.NewULID(), MailboxID: &alice.ID, DomainID: tenantDom.ID, Type: "alias", LocalPart: &lp, Target: "postmaster@tenant.example.com", Enabled: true}))
		// The DirectAdmin importer stores an external forward with its source
		// local part.
		refused(t, fwds.Create(ctx, &models.EmailForwarder{ID: ids.NewULID(), DomainID: tenantDom.ID, Type: "external", LocalPart: &lp, Target: "someone@elsewhere.example", Enabled: true}))
		require.Zero(t, count(t, "email_forwarders", tenantDom.ID))
		// An external forward of a mailbox has no local part and is unaffected.
		require.NoError(t, fwds.Create(ctx, &models.EmailForwarder{ID: ids.NewULID(), MailboxID: &alice.ID, DomainID: tenantDom.ID, Type: "external", Target: "alice@elsewhere.example", Enabled: true}))

		groups := repository.NewMailGroupRepository(gdb)
		refused(t, groups.Create(ctx, &models.MailGroup{ID: ids.NewULID(), DomainID: tenantDom.ID, LocalPart: "postmaster", GroupKind: "distribution", CreatedAt: now, UpdatedAt: now}))
		require.Zero(t, count(t, "mail_groups", tenantDom.ID))

		resources := repository.NewSharedResourceRepository(gdb)
		refused(t, resources.Create(ctx, &models.SharedResource{ID: ids.NewULID(), DomainID: tenantDom.ID, Kind: "mailbox", LocalPart: &lp}))
		require.Zero(t, count(t, "shared_resources", tenantDom.ID))

		// On the panel domain an alias at postmaster@ is the admin's choice.
		admin := panelPostmaster
		require.NoError(t, fwds.Create(ctx, &models.EmailForwarder{ID: ids.NewULID(), MailboxID: &admin.ID, DomainID: panelDom.ID, Type: "external", LocalPart: &lp, Target: "ops@elsewhere.example", Enabled: true}))
	})
}
