package domainops

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Package domain quota (JAB-279 AC1/AC2). The REST create door (createDomainOp,
// also used by automation) and the operator CLI (`jabali domain create`) each
// carried their own copy of this check. Both now call CheckDomainQuota after the
// owner-eligibility gate and before any allocation or persistence.
//
// Domains are an ordinary resource limit (GH #282): an owner with no package is
// unrestricted, and a package whose max_domains is 0 sets no limit. A package
// that cannot be read does not block the create either — the same choice the
// database-user quota makes. A failed domain count does block it.

var (
	// ErrDomainQuotaExceeded means the owner already has as many domains as the
	// package allows. The concrete numbers are carried by *DomainQuotaError.
	ErrDomainQuotaExceeded = errors.New("domainops: domain quota exceeded")
	// ErrDomainCount means the owner's domains could not be counted. The store
	// error is carried and printed; errors.Is matches both.
	ErrDomainCount = errors.New("domainops: could not count the owner's domains")
	// ErrQuotaDeps means a quota dependency is not wired — a wiring bug, not a
	// policy result.
	ErrQuotaDeps = errors.New("domainops: domain quota dependencies are not wired")
)

// DomainQuotaError carries the count and the limit of an exceeded quota.
// errors.Is(err, ErrDomainQuotaExceeded) matches it.
type DomainQuotaError struct {
	Count int64
	Max   uint32
}

func (e *DomainQuotaError) Error() string {
	return fmt.Sprintf("domainops: domain quota exceeded (%d/%d)", e.Count, e.Max)
}

func (e *DomainQuotaError) Unwrap() error { return ErrDomainQuotaExceeded }

// DomainCounter counts an owner's domains. repository's DomainRepository
// satisfies it.
type DomainCounter interface {
	CountByUserID(ctx context.Context, userID string) (int64, error)
}

// PackageFinder loads a hosting package. repository's PackageRepository
// satisfies it.
type PackageFinder interface {
	FindByID(ctx context.Context, id string) (*models.HostingPackage, error)
}

// QuotaDeps is what CheckDomainQuota reads.
type QuotaDeps struct {
	Domains  DomainCounter
	Packages PackageFinder
}

// CheckDomainQuota reports whether owner may acquire one more domain under its
// package. It returns nil when the owner is within the limit or unrestricted, a
// *DomainQuotaError when the limit is reached, ErrDomainCount (with the store
// error) when the count fails, and ErrOwnerNil or ErrQuotaDeps on a wiring bug.
// The count runs before the package lookup, as it did in the REST door.
func CheckDomainQuota(ctx context.Context, d QuotaDeps, owner *models.User) error {
	if owner == nil {
		return ErrOwnerNil
	}
	if owner.PackageID == nil || *owner.PackageID == "" {
		return nil
	}
	if d.Domains == nil || d.Packages == nil {
		return ErrQuotaDeps
	}
	count, err := d.Domains.CountByUserID(ctx, owner.ID)
	if err != nil {
		return &kindError{kind: ErrDomainCount, cause: err}
	}
	pkg, err := d.Packages.FindByID(ctx, *owner.PackageID)
	if err != nil || pkg == nil || pkg.MaxDomains == 0 {
		return nil
	}
	if count >= int64(pkg.MaxDomains) {
		return &DomainQuotaError{Count: count, Max: pkg.MaxDomains}
	}
	return nil
}
