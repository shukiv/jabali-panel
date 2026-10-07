package backupmetadata

import (
	"context"
	"errors"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a PHP pool restored from an uploaded file takes its process
// settings through the checks a tenant's pool edit runs: the pool page's
// limits and the owner's package cap.

// pcUsers holds the account being restored, with its package.
type pcUsers struct {
	repository.UserRepository
	pkg *string
	err error
}

func (r *pcUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &models.User{ID: id, PackageID: r.pkg}, nil
}

type pcPackages struct {
	repository.PackageRepository
	cap uint32
	err error
}

func (r *pcPackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &models.HostingPackage{ID: id, FpmMaxChildrenCap: r.cap}, nil
}

// pcUsersAfterFirst answers the first lookup (applyUser's) with the account,
// and every later one as pcUsers does.
type pcUsersAfterFirst struct {
	*pcUsers
	calls int
}

func (r *pcUsersAfterFirst) FindByID(ctx context.Context, id string) (*models.User, error) {
	r.calls++
	if r.calls == 1 {
		return &models.User{ID: id}, nil
	}
	return r.pcUsers.FindByID(ctx, id)
}

func pcMeta(mode string, children, idle uint32) *internalbackup.AccountMetadata {
	uname := "alice"
	return &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1", Email: "alice@example.com", Username: &uname},
		PHPPools: []internalbackup.MetadataPHPPool{{
			ID: "p-src", PHPVersion: "8.4", PmMode: mode, PmMaxChildren: children, ProcessIdleTimeoutSeconds: idle,
		}},
	}
}

func pcApply(meta *internalbackup.AccountMetadata, untrusted bool, users repository.UserRepository, pkgs repository.PackageRepository) (*ppPools, ApplyResult) {
	pools := &ppPools{}
	d := Deps{Users: users, PHPPools: pools, Untrusted: untrusted}
	if pkgs != nil {
		d.Packages = pkgs
	}
	return pools, Apply(context.Background(), meta, d)
}

func pcErrors(r ApplyResult, sub string) int {
	n := 0
	for _, e := range r.Errors {
		if strings.Contains(e, sub) {
			n++
		}
	}
	return n
}

func TestApply_UploadedPoolWithinTheLimitsKeepsItsSettings(t *testing.T) {
	pkg := "pkg1"
	pools, r := pcApply(pcMeta("dynamic", 8, 30), true, &pcUsers{pkg: &pkg}, &pcPackages{cap: 10})
	if len(r.Errors) != 0 || len(pools.rows) != 1 {
		t.Fatalf("errors %v pools %+v", r.Errors, pools.rows)
	}
	p := pools.rows[0]
	if p.PmMode != "dynamic" || p.PmMaxChildren != 8 || p.ProcessIdleTimeoutSeconds != 30 {
		t.Fatalf("pool %+v, want the backup's dynamic/8/30", p)
	}
	if p.PmMaxSpareServers == 0 || !(p.PmMinSpareServers <= p.PmStartServers && p.PmStartServers <= p.PmMaxSpareServers && p.PmMaxSpareServers <= p.PmMaxChildren) {
		t.Fatalf("dynamic spare servers %+v don't fit FPM's order", p)
	}
}

// Settings the pool page would refuse give way to the defaults.
func TestApply_UploadedPoolSettingsThePoolPageRefusesUseTheDefaults(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mode           string
		children, idle uint32
	}{
		{"unknown mode", "fork-bomb", 5, 30},
		{"too many children", "ondemand", 100000, 30},
		{"more children than a tenant may set", "ondemand", 150, 30},
		{"idle timeout too long", "ondemand", 5, 10000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pools, r := pcApply(pcMeta(tc.mode, tc.children, tc.idle), true, &pcUsers{}, &pcPackages{})
			if len(pools.rows) != 1 {
				t.Fatalf("pools %+v errors %v", pools.rows, r.Errors)
			}
			p := pools.rows[0]
			if p.PmMode != "ondemand" || p.PmMaxChildren != 20 || p.ProcessIdleTimeoutSeconds != 60 {
				t.Fatalf("pool %s/%d/%d, want the defaults ondemand/20/60", p.PmMode, p.PmMaxChildren, p.ProcessIdleTimeoutSeconds)
			}
			if pcErrors(r, "php_pool p-src: process settings not restored") != 1 {
				t.Fatalf("errors %v, want one report line", r.Errors)
			}
		})
	}
}

// The owner's package caps max children, as it does on the pool page.
func TestApply_UploadedPoolIsHeldToThePackageCap(t *testing.T) {
	pkg := "pkg1"
	pools, r := pcApply(pcMeta("dynamic", 50, 30), true, &pcUsers{pkg: &pkg}, &pcPackages{cap: 4})
	if len(pools.rows) != 1 {
		t.Fatalf("pools %+v errors %v", pools.rows, r.Errors)
	}
	p := pools.rows[0]
	if p.PmMaxChildren != 4 || p.PmMaxSpareServers > 4 || p.PmStartServers > 4 {
		t.Fatalf("pool %+v, want max children held to the package's 4", p)
	}
	if pcErrors(r, "php_pool p-src: max children lowered to 4") != 1 {
		t.Fatalf("errors %v, want the cap reported", r.Errors)
	}
}

// An account with no package has no package cap, only the pool page's.
func TestApply_UploadedPoolWithNoPackageHasOnlyThePoolPagesCap(t *testing.T) {
	pools, r := pcApply(pcMeta("ondemand", 60, 30), true, &pcUsers{}, &pcPackages{cap: 4})
	if len(r.Errors) != 0 || len(pools.rows) != 1 || pools.rows[0].PmMaxChildren != 60 {
		t.Fatalf("errors %v pools %+v, want 60 kept", r.Errors, pools.rows)
	}
}

// Fail closed: a cap Apply can't read keeps the pool out.
func TestApply_UploadedPoolIsNotRestoredWhenThePackageCantBeRead(t *testing.T) {
	pkg := "pkg1"
	for name, tc := range map[string]struct {
		users *pcUsers
		pkgs  repository.PackageRepository
	}{
		"package lookup fails": {&pcUsers{pkg: &pkg}, &pcPackages{err: errors.New("db down")}},
		"user lookup fails":    {&pcUsers{err: errors.New("db down")}, &pcPackages{}},
		"packages not wired":   {&pcUsers{pkg: &pkg}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			pools, r := pcApply(pcMeta("ondemand", 5, 30), true, &pcUsersAfterFirst{pcUsers: tc.users}, tc.pkgs)
			if len(pools.rows) != 0 || pcErrors(r, "php_pool p-src: not restored") != 1 {
				t.Fatalf("pools %+v errors %v, want the pool refused", pools.rows, r.Errors)
			}
		})
	}
}

// This server's own backups are not uploaded files: their pools come back as
// they were.
func TestApply_TrustedPoolKeepsItsSettings(t *testing.T) {
	pkg := "pkg1"
	pools, r := pcApply(pcMeta("static", 500, 30), false, &pcUsers{pkg: &pkg}, &pcPackages{cap: 4})
	if len(r.Errors) != 0 || len(pools.rows) != 1 || pools.rows[0].PmMaxChildren != 500 || pools.rows[0].PmMode != "static" {
		t.Fatalf("errors %v pools %+v, want static/500 kept", r.Errors, pools.rows)
	}
}
