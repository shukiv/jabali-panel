package backupmetadata

import (
	"context"
	"errors"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a domain restored from an uploaded file takes a PHP limit only
// when the account's package lets a tenant set it (GH #1701), as the PHP
// settings page does for the tenant.

// plpPackages answers with a package whose PHP policy is policy.
type plpPackages struct {
	repository.PackageRepository
	policy string
	err    error
	calls  int
}

func (r *plpPackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return &models.HostingPackage{ID: id, PHPSettingsPolicy: r.policy}, nil
}

func plpMeta(withLimits bool) *internalbackup.AccountMetadata {
	m := dcMeta()
	m.Domains = m.Domains[:1]
	m.Domains[0].Mailboxes = nil
	m.AppInstalls = nil
	if withLimits {
		mem, up, n := "512M", "64M", 3000
		m.Domains[0].PHPMemoryLimit, m.Domains[0].PHPUploadMaxFilesize, m.Domains[0].PHPMaxInputVars = &mem, &up, &n
	}
	return m
}

// plpApply restores meta as an uploaded file (or this server's own backup when
// trusted) into an account on package pkg (nil: none).
func plpApply(meta *internalbackup.AccountMetadata, trusted bool, pkg *string, pkgs repository.PackageRepository) (*dcDomains, ApplyResult) {
	dom, _, _, d := dcDeps()
	d.Users = &pcUsersAfterFirst{pcUsers: &pcUsers{pkg: pkg}}
	if pkgs != nil {
		d.Packages = pkgs
	}
	d.CheckDomain = func(context.Context, *models.Domain, string) ([]string, error) { return nil, nil }
	d.Untrusted = !trusted
	return dom, Apply(context.Background(), meta, d)
}

func plpLimits(dom *dcDomains) (mem, up *string, vars *int) {
	if len(dom.created) != 1 {
		return nil, nil, nil
	}
	c := dom.created[0]
	return c.PHPMemoryLimit, c.PHPUploadMaxFilesize, c.PHPMaxInputVars
}

func TestApply_UploadedDomainTakesOnlyThePHPLimitsItsPackageLetsATenantSet(t *testing.T) {
	pkg := "pkg1"
	dom, r := plpApply(plpMeta(true), false, &pkg, &plpPackages{policy: `{"memory_limit":"admin_only","max_input_vars":"admin_only"}`})

	mem, up, vars := plpLimits(dom)
	if len(dom.created) != 1 || mem != nil || vars != nil || up == nil || *up != "64M" {
		t.Fatalf("created %d, memory %v, input vars %v, upload %v; want memory and input vars dropped, upload kept (errors %v)", len(dom.created), mem, vars, up, r.Errors)
	}
	for _, want := range []string{
		"domain d-good (good.org): PHP memory_limit not restored: the account's package lets only an administrator set it",
		"domain d-good (good.org): PHP max_input_vars not restored: the account's package lets only an administrator set it",
	} {
		if !hasError(r.Errors, want) {
			t.Errorf("errors %v, want %q", r.Errors, want)
		}
	}
	if hasError(r.Errors, "upload_max_filesize") {
		t.Errorf("errors %v: the permitted upload limit was reported", r.Errors)
	}
}

func TestApply_UploadedDomainWithoutAPackageTakesItsPHPLimits(t *testing.T) {
	dom, r := plpApply(plpMeta(true), false, nil, &plpPackages{})
	mem, up, vars := plpLimits(dom)
	if mem == nil || *mem != "512M" || up == nil || vars == nil || *vars != 3000 || hasError(r.Errors, "PHP ") {
		t.Fatalf("memory %v upload %v input vars %v errors %v; want all three taken", mem, up, vars, r.Errors)
	}
}

// When the package can't be read, no limit is taken from the file.
func TestApply_UploadedDomainTakesNoPHPLimitWhenThePackageCantBeRead(t *testing.T) {
	pkg := "pkg1"
	for name, pkgs := range map[string]repository.PackageRepository{
		"lookup fails": &plpPackages{err: errors.New("db down")},
		"not wired":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			dom, r := plpApply(plpMeta(true), false, &pkg, pkgs)
			mem, up, vars := plpLimits(dom)
			if len(dom.created) != 1 || mem != nil || up != nil || vars != nil {
				t.Fatalf("created %d, memory %v upload %v input vars %v; want the domain without limits (errors %v)", len(dom.created), mem, up, vars, r.Errors)
			}
			if !hasError(r.Errors, "PHP memory_limit not restored: the account's package could not be read") {
				t.Fatalf("errors %v, want the dropped limits reported", r.Errors)
			}
		})
	}
}

// A domain without limits needs no package.
func TestApply_UploadedDomainWithoutPHPLimitsReadsNoPackage(t *testing.T) {
	pkg := "pkg1"
	pkgs := &plpPackages{err: errors.New("db down")}
	dom, r := plpApply(plpMeta(false), false, &pkg, pkgs)
	if len(dom.created) != 1 || pkgs.calls != 0 || hasError(r.Errors, "PHP ") {
		t.Fatalf("created %d, package lookups %d, errors %v; want the domain and no lookup", len(dom.created), pkgs.calls, r.Errors)
	}
}

func TestApply_TrustedDomainKeepsItsPHPLimits(t *testing.T) {
	pkg := "pkg1"
	dom, r := plpApply(plpMeta(true), true, &pkg, &plpPackages{policy: `{"memory_limit":"admin_only"}`})
	if mem, _, _ := plpLimits(dom); mem == nil || *mem != "512M" || hasError(r.Errors, "PHP ") {
		t.Fatalf("memory %v errors %v; want this server's own backup restored as it is", mem, r.Errors)
	}
}

// Every limit the backup carries is held to the policy.
func TestApply_UploadedDomainHoldsEveryPHPLimitToItsPackage(t *testing.T) {
	m := plpMeta(false)
	s1, s2, s3, n1, n2, n3 := "512M", "64M", "64M", 3000, 300, 300
	dm := &m.Domains[0]
	dm.PHPMemoryLimit, dm.PHPUploadMaxFilesize, dm.PHPPostMaxSize = &s1, &s2, &s3
	dm.PHPMaxInputVars, dm.PHPMaxExecutionTime, dm.PHPMaxInputTime = &n1, &n2, &n3
	pkg := "pkg1"
	locked := `{"memory_limit":"admin_only","upload_max_filesize":"admin_only","post_max_size":"admin_only",` +
		`"max_input_vars":"admin_only","max_execution_time":"admin_only","max_input_time":"admin_only"}`
	dom, r := plpApply(m, false, &pkg, &plpPackages{policy: locked})
	if len(dom.created) != 1 {
		t.Fatalf("created %d (errors %v)", len(dom.created), r.Errors)
	}
	c := dom.created[0]
	if c.PHPMemoryLimit != nil || c.PHPUploadMaxFilesize != nil || c.PHPPostMaxSize != nil ||
		c.PHPMaxInputVars != nil || c.PHPMaxExecutionTime != nil || c.PHPMaxInputTime != nil {
		t.Fatalf("created %+v, want every PHP limit dropped", c)
	}
	for _, d := range []string{"memory_limit", "upload_max_filesize", "post_max_size", "max_input_vars", "max_execution_time", "max_input_time"} {
		if !hasError(r.Errors, "PHP "+d+" not restored") {
			t.Errorf("errors %v, want %s reported", r.Errors, d)
		}
	}
}
