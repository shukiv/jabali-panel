package backupmetadata

import (
	"context"
	"errors"
	"reflect"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: with "Overwrite existing items with the backup" checked, a PHP
// pool the account already has takes the backup's process settings and PHP
// settings, through the checks the pool page runs. An uploaded file's PHP
// settings pass the pool page's checks whatever the mode.

// owpPools hands out copies, as the database does, and records updates.
type owpPools struct {
	*ppPools
	updates int
}

func (r *owpPools) FindByID(ctx context.Context, id string) (*models.PHPPool, error) {
	p, err := r.ppPools.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	c := *p
	return &c, nil
}

func (r *owpPools) FindByUserAndVersion(ctx context.Context, userID, version string) (*models.PHPPool, error) {
	p, err := r.ppPools.FindByUserAndVersion(ctx, userID, version)
	if err != nil {
		return nil, err
	}
	c := *p
	return &c, nil
}

func (r *owpPools) Update(_ context.Context, p *models.PHPPool) error {
	r.updates++
	for i := range r.rows {
		if r.rows[i].ID == p.ID {
			r.rows[i] = *p
			return nil
		}
	}
	return repository.ErrNotFound
}

// owpIni records updates to the overrides a pool has.
type owpIni struct {
	*ppIni
	updates int
}

func (r *owpIni) Update(_ context.Context, o *models.PHPPoolIniOverride) error {
	r.updates++
	for i := range r.rows {
		if r.rows[i].ID == o.ID {
			r.rows[i] = *o
			return nil
		}
	}
	return repository.ErrNotFound
}

func (r *owpIni) value(poolID, directive string) (string, bool) {
	for _, o := range r.rows {
		if o.PoolID == poolID && o.Directive == directive {
			return o.Value, true
		}
	}
	return "", false
}

// owpOwnPool is the account's pool before the restore: ondemand, 5 children,
// a 10 second idle timeout.
func owpOwnPool(id string) models.PHPPool {
	return models.PHPPool{
		ID: id, UserID: "u1", PHPVersion: "8.4", PmMode: "ondemand", PmMaxChildren: 5,
		ProcessIdleTimeoutSeconds: 10, PmMaxRequests: 500, Status: "active",
	}
}

// owpApply restores meta from an uploaded file into an account on a package
// with an FPM cap of 10, with Overwrite checked or not.
func owpApply(meta *internalbackup.AccountMetadata, overwrite bool, pools *owpPools, ini *owpIni, pkgs repository.PackageRepository) ApplyResult {
	pkg := "pkg1"
	if pkgs == nil {
		pkgs = &pcPackages{cap: 10}
	}
	d := Deps{
		Users: &pcUsers{pkg: &pkg}, Packages: pkgs, PHPPools: pools,
		Untrusted: true, OverwriteRows: overwrite, KeepExisting: !overwrite,
	}
	if ini != nil {
		d.PHPPoolIni = ini
	}
	return Apply(context.Background(), meta, d)
}

// The account's pool takes the backup's process settings, whether it has the
// backup pool's id or is the account's pool of the same PHP version.
func TestApply_OverwriteGivesTheAccountsPoolTheBackupsProcessSettings(t *testing.T) {
	for name, id := range map[string]string{"same id": "p-src", "same PHP version": "p-own"} {
		t.Run(name, func(t *testing.T) {
			pools := &owpPools{ppPools: &ppPools{rows: []models.PHPPool{owpOwnPool(id)}}}
			r := owpApply(pcMeta("dynamic", 8, 30), true, pools, nil, nil)

			if len(r.Errors) != 0 || len(pools.rows) != 1 || pools.updates != 1 {
				t.Fatalf("errors %v pools %+v updates %d, want the one pool updated", r.Errors, pools.rows, pools.updates)
			}
			p := pools.rows[0]
			if p.ID != id || p.PmMode != "dynamic" || p.PmMaxChildren != 8 || p.ProcessIdleTimeoutSeconds != 30 {
				t.Fatalf("pool %+v, want %s with the backup's dynamic/8/30", p, id)
			}
			if p.PmMaxSpareServers == 0 || !(p.PmMinSpareServers <= p.PmStartServers && p.PmStartServers <= p.PmMaxSpareServers && p.PmMaxSpareServers <= p.PmMaxChildren) {
				t.Fatalf("dynamic spare servers %+v don't fit FPM's order", p)
			}
			if p.Status != "pending" {
				t.Fatalf("status %q, want pending so the pool is applied again", p.Status)
			}
		})
	}
}

func TestApply_OverwritePoolSettingsAreHeldToThePackageCap(t *testing.T) {
	pools := &owpPools{ppPools: &ppPools{rows: []models.PHPPool{owpOwnPool("p-own")}}}
	r := owpApply(pcMeta("dynamic", 50, 30), true, pools, nil, &pcPackages{cap: 4})
	p := pools.rows[0]
	if p.PmMode != "dynamic" || p.PmMaxChildren != 4 || p.PmMaxSpareServers > 4 || p.PmStartServers > 4 {
		t.Fatalf("pool %+v, want dynamic held to the package's 4", p)
	}
	if pcErrors(r, "php_pool p-src: max children lowered to 4") != 1 {
		t.Fatalf("errors %v, want the cap reported", r.Errors)
	}
}

// Settings the pool page would refuse don't replace the pool's own, and
// neither do settings whose package cap can't be read.
func TestApply_OverwritePoolKeepsItsSettingsWhenTheBackupsCantBeTaken(t *testing.T) {
	for name, tc := range map[string]struct {
		meta *internalbackup.AccountMetadata
		pkgs repository.PackageRepository
		want string
	}{
		"unknown mode":          {pcMeta("fork-bomb", 5, 30), nil, "php_pool p-src: process settings not updated ("},
		"too many children":     {pcMeta("ondemand", 150, 30), nil, "php_pool p-src: process settings not updated ("},
		"idle timeout too long": {pcMeta("ondemand", 5, 10000000), nil, "php_pool p-src: process settings not updated ("},
		"package lookup fails":  {pcMeta("dynamic", 8, 30), &pcPackages{err: errors.New("db down")}, "php_pool p-src: process settings not updated: "},
	} {
		t.Run(name, func(t *testing.T) {
			pools := &owpPools{ppPools: &ppPools{rows: []models.PHPPool{owpOwnPool("p-own")}}}
			r := owpApply(tc.meta, true, pools, nil, tc.pkgs)
			if p := pools.rows[0]; !reflect.DeepEqual(p, owpOwnPool("p-own")) || pools.updates != 0 {
				t.Fatalf("pool %+v updates %d, want it as it was", p, pools.updates)
			}
			if pcErrors(r, tc.want) != 1 {
				t.Fatalf("errors %v, want %q", r.Errors, tc.want)
			}
		})
	}
}

// Without Overwrite, or when the backup has the pool's settings, the pool
// isn't touched.
func TestApply_PoolIsNotUpdatedWithoutOverwriteOrWithoutChanges(t *testing.T) {
	for name, tc := range map[string]struct {
		meta      *internalbackup.AccountMetadata
		overwrite bool
		pkgs      repository.PackageRepository
	}{
		"keep existing":     {pcMeta("dynamic", 8, 30), false, nil},
		"the same settings": {pcMeta("ondemand", 5, 10), true, nil},
		// Nothing to take needs no package cap.
		"the same settings, package unreadable": {pcMeta("ondemand", 5, 10), true, &pcPackages{err: errors.New("db down")}},
	} {
		t.Run(name, func(t *testing.T) {
			pools := &owpPools{ppPools: &ppPools{rows: []models.PHPPool{owpOwnPool("p-own")}}}
			r := owpApply(tc.meta, tc.overwrite, pools, nil, tc.pkgs)
			if p := pools.rows[0]; !reflect.DeepEqual(p, owpOwnPool("p-own")) || pools.updates != 0 || len(r.Errors) != 0 {
				t.Fatalf("pool %+v updates %d errors %v, want it as it was", p, pools.updates, r.Errors)
			}
		})
	}
}

func owpIniMeta(overrides ...internalbackup.MetadataPHPPoolIniOverride) *internalbackup.AccountMetadata {
	m := pcMeta("ondemand", 5, 10)
	m.PHPPools[0].IniOverrides = overrides
	return m
}

func owpOwnIni() *owpIni {
	return &owpIni{ppIni: &ppIni{rows: []models.PHPPoolIniOverride{
		{ID: "o-mem", PoolID: "p-own", Directive: "memory_limit", Value: "256M", Kind: "value"},
		{ID: "o-up", PoolID: "p-own", Directive: "upload_max_filesize", Value: "32M", Kind: "value"},
	}}}
}

// With Overwrite, a PHP setting both have takes the backup's value; one only
// the backup has is added, and one only the pool has stays. The pool is
// applied again.
func TestApply_OverwritePoolSettingTakesTheBackupsValue(t *testing.T) {
	pools := &owpPools{ppPools: &ppPools{rows: []models.PHPPool{owpOwnPool("p-own")}}}
	ini := owpOwnIni()
	r := owpApply(owpIniMeta(
		internalbackup.MetadataPHPPoolIniOverride{ID: "o1", Directive: "memory_limit", Value: "768M", Kind: "value"},
		internalbackup.MetadataPHPPoolIniOverride{ID: "o2", Directive: "max_execution_time", Value: "120", Kind: "value"},
	), true, pools, ini, nil)

	if len(r.Errors) != 0 {
		t.Fatalf("errors %v", r.Errors)
	}
	for directive, want := range map[string]string{"memory_limit": "768M", "max_execution_time": "120", "upload_max_filesize": "32M"} {
		if got, ok := ini.value("p-own", directive); !ok || got != want {
			t.Errorf("%s = %q (set %v), want %q; overrides %+v", directive, got, ok, want, ini.rows)
		}
	}
	if len(ini.rows) != 3 || ini.updates != 1 {
		t.Fatalf("overrides %+v updates %d, want memory_limit updated in place and one added", ini.rows, ini.updates)
	}
	if pools.rows[0].Status != "pending" || pools.updates != 1 {
		t.Fatalf("pool %+v updates %d, want it pending so it is applied again", pools.rows[0], pools.updates)
	}
}

// A changed setting alone has the pool applied again.
func TestApply_OverwritePoolSettingAloneHasThePoolAppliedAgain(t *testing.T) {
	pools := &owpPools{ppPools: &ppPools{rows: []models.PHPPool{owpOwnPool("p-own")}}}
	ini := owpOwnIni()
	r := owpApply(owpIniMeta(
		internalbackup.MetadataPHPPoolIniOverride{ID: "o1", Directive: "memory_limit", Value: "768M", Kind: "value"},
	), true, pools, ini, nil)
	if got, _ := ini.value("p-own", "memory_limit"); got != "768M" || pools.rows[0].Status != "pending" || pools.updates != 1 {
		t.Fatalf("memory_limit = %q pool %+v updates %d errors %v, want 768M and the pool pending", got, pools.rows[0], pools.updates, r.Errors)
	}
}

// Without Overwrite a setting the pool has keeps its value. An added one still
// has the pool applied again, or it would not reach PHP.
func TestApply_KeepExistingPoolSettingKeepsItsValue(t *testing.T) {
	pools := &owpPools{ppPools: &ppPools{rows: []models.PHPPool{owpOwnPool("p-own")}}}
	ini := owpOwnIni()
	r := owpApply(owpIniMeta(
		internalbackup.MetadataPHPPoolIniOverride{ID: "o1", Directive: "memory_limit", Value: "768M", Kind: "value"},
		internalbackup.MetadataPHPPoolIniOverride{ID: "o2", Directive: "max_execution_time", Value: "120", Kind: "value"},
	), false, pools, ini, nil)

	if got, _ := ini.value("p-own", "memory_limit"); got != "256M" || ini.updates != 0 {
		t.Fatalf("memory_limit = %q updates %d, want 256M kept (errors %v)", got, ini.updates, r.Errors)
	}
	if got, _ := ini.value("p-own", "max_execution_time"); got != "120" {
		t.Fatalf("max_execution_time = %q, want 120 added", got)
	}
	if pools.rows[0].Status != "pending" || pools.rows[0].PmMaxChildren != 5 {
		t.Fatalf("pool %+v, want its own settings, pending", pools.rows[0])
	}
}

// Nothing to add or change: the pool isn't applied again.
func TestApply_PoolWithEveryBackupSettingIsNotAppliedAgain(t *testing.T) {
	for name, overwrite := range map[string]bool{"keep existing": false, "overwrite": true} {
		t.Run(name, func(t *testing.T) {
			pools := &owpPools{ppPools: &ppPools{rows: []models.PHPPool{owpOwnPool("p-own")}}}
			ini := owpOwnIni()
			r := owpApply(owpIniMeta(
				internalbackup.MetadataPHPPoolIniOverride{ID: "o1", Directive: "memory_limit", Value: "256M", Kind: "value"},
			), overwrite, pools, ini, nil)
			if pools.updates != 0 || ini.updates != 0 || len(ini.rows) != 2 || len(r.Errors) != 0 {
				t.Fatalf("pool updates %d override updates %d overrides %+v errors %v, want nothing changed", pools.updates, ini.updates, ini.rows, r.Errors)
			}
		})
	}
}

// A PHP setting from an uploaded file passes the pool page's checks, on a
// pool the restore creates and on one the account has, in either mode.
func TestApply_UploadedPoolSettingsPassThePoolPagesChecks(t *testing.T) {
	bad := []internalbackup.MetadataPHPPoolIniOverride{
		{ID: "o-ctl", Directive: "error_log", Value: "/tmp/x\nphp_admin_value[open_basedir]=/", Kind: "value"},
		{ID: "o-flag", Directive: "display_errors", Value: "maybe", Kind: "flag"},
		{ID: "o-kind", Directive: "max_input_vars", Value: "3000", Kind: "admin"},
		{ID: "o-name", Directive: "", Value: "1", Kind: "value"},
		{ID: "o-mem", Directive: "memory_limit", Value: "768M\r", Kind: "value"},
	}
	good := []internalbackup.MetadataPHPPoolIniOverride{
		{ID: "o-ok", Directive: "max_execution_time", Value: "120", Kind: "value"},
		{ID: "o-on", Directive: "short_open_tag", Value: " ON ", Kind: "flag"},
	}
	wantLines := []string{
		"php_pool_ini o-ctl: not restored: value must not contain control characters",
		"php_pool_ini o-flag: not restored: a flag value must be 'on' or 'off'",
		"php_pool_ini o-kind: not restored: kind must be 'value' or 'flag'",
		"php_pool_ini o-name: not restored: directive is required",
		"php_pool_ini o-mem: not restored: value must not contain control characters",
	}
	for name, tc := range map[string]struct {
		pools     []models.PHPPool
		poolID    string
		overwrite bool
	}{
		"created pool":             {nil, "p-src", false},
		"own pool, keep existing":  {[]models.PHPPool{owpOwnPool("p-own")}, "p-own", false},
		"own pool, with overwrite": {[]models.PHPPool{owpOwnPool("p-own")}, "p-own", true},
	} {
		t.Run(name, func(t *testing.T) {
			pools := &owpPools{ppPools: &ppPools{rows: tc.pools}}
			ini := &owpIni{ppIni: &ppIni{}}
			if tc.pools != nil {
				ini = owpOwnIni()
			}
			r := owpApply(owpIniMeta(append(append([]internalbackup.MetadataPHPPoolIniOverride{}, bad...), good...)...), tc.overwrite, pools, ini, nil)

			for _, want := range wantLines {
				if pcErrors(r, want) != 1 {
					t.Errorf("errors %v, want %q", r.Errors, want)
				}
			}
			if got, _ := ini.value(tc.poolID, "max_execution_time"); got != "120" {
				t.Errorf("max_execution_time = %q, want 120", got)
			}
			if got, _ := ini.value(tc.poolID, "short_open_tag"); got != "on" {
				t.Errorf("short_open_tag = %q, want the pool page's \"on\"", got)
			}
			for _, o := range ini.rows {
				if o.Directive == "" || o.Directive == "error_log" || o.Directive == "display_errors" || o.Directive == "max_input_vars" {
					t.Errorf("override %+v restored, want it refused", o)
				}
			}
			if tc.pools != nil {
				if got, _ := ini.value(tc.poolID, "memory_limit"); got != "256M" {
					t.Errorf("memory_limit = %q, want the pool's 256M kept over a refused value", got)
				}
			}
		})
	}
}

// This server's own backups are not uploaded files; their PHP settings were
// checked when they were set.
func TestApply_TrustedPoolSettingsAreRestoredAsTheyAre(t *testing.T) {
	pools := &ppPools{}
	ini := &ppIni{}
	meta := owpIniMeta(internalbackup.MetadataPHPPoolIniOverride{ID: "o-kind", Directive: "max_input_vars", Value: "3000", Kind: ""})
	r := Apply(context.Background(), meta, Deps{Users: &pcUsers{}, PHPPools: pools, PHPPoolIni: ini})
	if len(ini.rows) != 1 || len(r.Errors) != 0 {
		t.Fatalf("overrides %+v errors %v, want the backup's setting restored", ini.rows, r.Errors)
	}
}

// A created pool whose settings the pool page refuses takes the defaults, and
// those too are held to the package cap.
func TestApply_UploadedPoolDefaultsAreHeldToThePackageCap(t *testing.T) {
	pkg := "pkg1"
	pools, r := pcApply(pcMeta("fork-bomb", 5, 30), true, &pcUsers{pkg: &pkg}, &pcPackages{cap: 4})
	if len(pools.rows) != 1 || pools.rows[0].PmMaxChildren != 4 {
		t.Fatalf("pools %+v errors %v, want the defaults held to 4", pools.rows, r.Errors)
	}
	for _, want := range []string{"php_pool p-src: process settings not restored", "php_pool p-src: max children lowered to 4"} {
		if pcErrors(r, want) != 1 {
			t.Errorf("errors %v, want %q", r.Errors, want)
		}
	}
}
