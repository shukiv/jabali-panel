package dbops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type capUsers struct {
	repository.UserRepository
	u *models.User
}

func (f capUsers) FindByID(context.Context, string) (*models.User, error) { return f.u, nil }

// capDatabases passes the early count; insertErr is what the row insert
// returns.
type capDatabases struct {
	repository.DatabaseRepository
	count     int64
	insertErr error
	inserted  bool
	capped    bool // the insert went through CreateWithinCap
}

func (f *capDatabases) CountByUserID(context.Context, string) (int64, error) { return f.count, nil }
func (f *capDatabases) ExistsByUserAndName(context.Context, string, string) (bool, error) {
	return false, nil
}
func (f *capDatabases) Create(context.Context, *models.Database) error {
	f.inserted = f.insertErr == nil
	return f.insertErr
}
func (f *capDatabases) CreateWithinCap(ctx context.Context, d *models.Database, _ uint32) error {
	f.capped = true
	return f.Create(ctx, d)
}

func capDeps(ag AgentCaller, dbs *capDatabases, pkgs repository.PackageRepository) Deps {
	pkgID, uname := "pkg1", "alice"
	return Deps{
		Users:     capUsers{u: &models.User{ID: "u1", Username: &uname, PackageID: &pkgID}},
		Packages:  pkgs,
		Databases: dbs,
		Agent:     ag,
	}
}

// Another create took the last slot between the early count and the insert:
// the locked insert refuses, and the database this call made is dropped.
func TestCreate_CapReachedAtInsert_DropsTheNewDatabase(t *testing.T) {
	ag := &recAgent{}
	dbs := &capDatabases{count: 2, insertErr: repository.ErrDatabaseCapExceeded}
	pkgs := limitPkgs{pkg: &models.HostingPackage{MaxDatabases: 3}}
	_, err := Create(context.Background(), capDeps(ag, dbs, pkgs), CreateInput{UserID: "u1", RawName: "shop"})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err = %v, want ErrQuotaExceeded", err)
	}
	if !dbs.capped || dbs.inserted {
		t.Fatalf("insert must go through the cap check and fail: capped %v inserted %v", dbs.capped, dbs.inserted)
	}
	if len(ag.calls) != 2 || ag.calls[0] != "db.create" || ag.calls[1] != "db.drop" {
		t.Fatalf("want db.create then db.drop, got %v", ag.calls)
	}
}

// Any failed insert drops the database it made, so no database is left
// without a row.
func TestCreate_InsertFails_DropsTheNewDatabase(t *testing.T) {
	ag := &recAgent{}
	dbs := &capDatabases{insertErr: errors.New("insert boom")}
	_, err := Create(context.Background(), capDeps(ag, dbs, limitPkgs{pkg: &models.HostingPackage{}}), CreateInput{UserID: "u1", RawName: "shop"})
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("err = %v, want ErrInternal", err)
	}
	if len(ag.calls) != 2 || ag.calls[0] != "db.create" || ag.calls[1] != "db.drop" {
		t.Fatalf("want db.create then db.drop, got %v", ag.calls)
	}
}

// A package that is set but can't be loaded refuses before the agent.
func TestCreate_PackageLookupError_Refuses(t *testing.T) {
	ag := &recAgent{}
	dbs := &capDatabases{}
	_, err := Create(context.Background(), capDeps(ag, dbs, limitPkgs{err: errors.New("db down")}), CreateInput{UserID: "u1", RawName: "shop"})
	if !errors.Is(err, ErrQuotaUnavailable) {
		t.Fatalf("err = %v, want ErrQuotaUnavailable", err)
	}
	if len(ag.calls) != 0 || dbs.inserted {
		t.Fatalf("created anyway: calls %v inserted %v", ag.calls, dbs.inserted)
	}
}

// No cap: the plain insert, no lock.
func TestCreate_NoCap_PlainInsert(t *testing.T) {
	ag := &recAgent{}
	dbs := &capDatabases{count: 99}
	if _, err := Create(context.Background(), capDeps(ag, dbs, limitPkgs{err: repository.ErrNotFound}), CreateInput{UserID: "u1", RawName: "shop"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if dbs.capped || !dbs.inserted {
		t.Fatalf("capped %v inserted %v, want a plain insert", dbs.capped, dbs.inserted)
	}
}
