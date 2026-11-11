package main

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailboxops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// The CLI tests have no database: a create there applies no package limit
// unless a test feeds one.
func init() {
	cliMailboxLimits = func() (repository.UserRepository, repository.PackageRepository) { return nil, nil }
}

type cliLimitUsers struct{ repository.UserRepository }

func (cliLimitUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	pkg := "p1"
	return &models.User{ID: id, PackageID: &pkg}, nil
}

type cliLimitPackages struct {
	repository.PackageRepository
	max uint32
}

func (p cliLimitPackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	return &models.HostingPackage{ID: id, MaxEmailAccounts: p.max}, nil
}

// `jabali mailbox create` is held to the owner's package limit on email
// accounts, like `jabali db create` is to the database limit.
func TestCreateMailbox_PackageEmailAccountLimit(t *testing.T) {
	prev := cliMailboxLimits
	t.Cleanup(func() { cliMailboxLimits = prev })
	cliMailboxLimits = func() (repository.UserRepository, repository.PackageRepository) {
		return cliLimitUsers{}, cliLimitPackages{max: 1}
	}

	repo := newFakeMailboxRepo()
	preseedMailbox(repo, "mb1", "dom1", "example.com", "first")

	_, _, err := createMailboxDirect(context.Background(), repo, nil, nil, testDomain("dom1", "example.com", true), "second", "", 0, "", false)
	if !errors.Is(err, mailboxops.ErrQuotaExceeded) {
		t.Fatalf("want ErrQuotaExceeded, got %v", err)
	}
	if len(repo.rows) != 1 {
		t.Fatalf("a row was written past the limit: %d rows", len(repo.rows))
	}
}
