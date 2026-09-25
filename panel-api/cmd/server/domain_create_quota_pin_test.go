package main

import (
	"context"
	"errors"
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
