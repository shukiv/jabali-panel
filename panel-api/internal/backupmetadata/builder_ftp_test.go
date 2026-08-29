package backupmetadata

import (
	"context"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type stubFtpRepo struct {
	repository.FtpAccountRepository
	rows []models.FtpAccount
	// captured Create rows for the apply test
	created []*models.FtpAccount
}

func (s *stubFtpRepo) ListByUserID(context.Context, string) ([]models.FtpAccount, error) {
	return s.rows, nil
}
func (s *stubFtpRepo) Create(_ context.Context, a *models.FtpAccount) error {
	s.created = append(s.created, a)
	return nil
}

func uptr(v uint32) *uint32 { return &v }

// GH #1361: Build captures the account's FTP subaccount rows (with UID /
// JailPath / Isolated preserved). PasswordShadow stays empty on the panel-built
// bundle (the agent fills it from /etc/shadow later).
func TestBuild_CapturesFtpAccounts(t *testing.T) {
	repo := &stubFtpRepo{rows: []models.FtpAccount{
		{ID: "f1", Username: "t1_web", HomePath: "/home/t1/site", FTPAccess: true, SFTPAccess: true,
			IsEnabled: true, UID: uptr(50001), Isolated: true, QuotaMB: 500, JailPath: "/var/lib/jabali-ftp-jails/t1/web"},
		{ID: "f2", Username: "t1_legacy", HomePath: "/home/t1", FTPAccess: true, IsEnabled: true},
	}}
	m := Build(context.Background(), &models.User{ID: "u1"}, Deps{FtpAccounts: repo})
	if len(m.FtpAccounts) != 2 {
		t.Fatalf("want 2 ftp accounts, got %d", len(m.FtpAccounts))
	}
	a := m.FtpAccounts[0]
	if a.Username != "t1_web" || a.UID == nil || *a.UID != 50001 || !a.Isolated ||
		a.JailPath != "/var/lib/jabali-ftp-jails/t1/web" || a.QuotaMB != 500 || !a.SFTPAccess {
		t.Fatalf("isolated row not captured faithfully: %#v", a)
	}
	if a.PasswordShadow != "" {
		t.Error("panel-built bundle must NOT carry a shadow hash")
	}
	if m.FtpAccounts[1].UID != nil {
		t.Error("legacy row must keep UID nil")
	}
}

// Apply rebuilds the rows owned by the restored account.
func TestApply_RebuildsFtpAccounts(t *testing.T) {
	repo := &stubFtpRepo{}
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1"},
		FtpAccounts: []internalbackup.MetadataFtpAccount{
			{ID: "f1", Username: "t1_web", HomePath: "/home/t1/site", FTPAccess: true, SFTPAccess: true,
				IsEnabled: true, UID: uptr(50001), Isolated: true, QuotaMB: 500, JailPath: "/j"},
		},
	}
	r := Apply(context.Background(), meta, Deps{Users: existingUsersRepo{}, FtpAccounts: repo})
	if len(r.Errors) != 0 {
		t.Fatalf("apply errors: %v", r.Errors)
	}
	if r.FtpAccounts != 1 || len(repo.created) != 1 {
		t.Fatalf("expected 1 ftp account rebuilt, got count=%d created=%d", r.FtpAccounts, len(repo.created))
	}
	got := repo.created[0]
	if got.UserID != "u1" || got.Username != "t1_web" || got.UID == nil || *got.UID != 50001 ||
		!got.Isolated || got.JailPath != "/j" || got.QuotaMB != 500 || !got.SFTPAccess {
		t.Fatalf("rebuilt row wrong: %#v", got)
	}
}
