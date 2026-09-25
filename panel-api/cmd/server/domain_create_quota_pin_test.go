package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type quotaCounter struct {
	count int64
	err   error
}

func (q quotaCounter) CountByUserID(context.Context, string) (int64, error) { return q.count, q.err }

type quotaPackages struct{ max uint32 }

func (q quotaPackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	return &models.HostingPackage{ID: id, MaxDomains: q.max}, nil
}

// TestCLIDomainQuotaError_Messages drives the shared domainops.CheckDomainQuota
// and pins that `jabali domain create` prints the message it printed when the
// check was inline (JAB-279).
func TestCLIDomainQuotaError_Messages(t *testing.T) {
	pkg := "pkg-1"
	owner := &models.User{ID: "u1", PackageID: &pkg}

	err := domainops.CheckDomainQuota(context.Background(), domainops.QuotaDeps{
		Domains: quotaCounter{count: 3}, Packages: quotaPackages{max: 3},
	}, owner)
	if got, want := cliDomainQuotaError(err).Error(), "package quota exceeded: 3/3 domains"; got != want {
		t.Fatalf("quota message = %q, want %q", got, want)
	}

	cause := errors.New("connection refused")
	err = domainops.CheckDomainQuota(context.Background(), domainops.QuotaDeps{
		Domains: quotaCounter{err: cause}, Packages: quotaPackages{max: 3},
	}, owner)
	mapped := cliDomainQuotaError(err)
	if got, want := mapped.Error(), "count existing domains: connection refused"; got != want {
		t.Fatalf("count message = %q, want %q", got, want)
	}
	if !errors.Is(mapped, cause) {
		t.Fatal("the count message must wrap the store error")
	}

	other := errors.New("other")
	if cliDomainQuotaError(other) != other {
		t.Fatal("an unmapped error must pass through")
	}
}

// TestCLICreateDomain_RunsDomainQuota source-pins that createDomainDirect runs
// the shared quota check after the owner-eligibility gate and before any
// service resolution or persistence. createDomainDirect calls initConfig /
// initDB and is not unit-testable; the check itself is proven by
// domainops.TestCheckDomainQuota.
func TestCLICreateDomain_RunsDomainQuota(t *testing.T) {
	src := stripLineComments(readGoSource(t, "cli_create.go"))

	quotaIdx := strings.Index(src, "domainops.CheckDomainQuota(ctx, domainops.QuotaDeps{")
	if quotaIdx < 0 {
		t.Fatal("CLI create must run domainops.CheckDomainQuota")
	}
	if !strings.Contains(src, "Packages: packageRepoFromDB(),") {
		t.Error("CLI create must feed the quota check the real package repository")
	}
	if !strings.Contains(src, "cliDomainQuotaError(err)") {
		t.Error("CLI create must map the quota rejection through cliDomainQuotaError")
	}
	if i := strings.Index(src, "domainops.CheckOwnerEligible(owner)"); i < 0 || i > quotaIdx {
		t.Error("the owner-eligibility gate must run before the quota check")
	}
	for _, after := range []string{"domainops.ResolveMailPosture(", "domainops.PersistDomain("} {
		if i := strings.Index(src, after); i < 0 || i < quotaIdx {
			t.Errorf("the quota check must run before %s", after)
		}
	}
	if strings.Contains(src, ".MaxDomains") {
		t.Error("CLI create must not compare against MaxDomains inline; domainops owns the quota")
	}
}
