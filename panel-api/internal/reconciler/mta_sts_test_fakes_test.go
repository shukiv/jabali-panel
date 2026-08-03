package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeSSLCertRepo is the minimum-viable SSLCertificateRepository for
// mta_sts_reconcile_test. Only FindByDomainID is exercised; the rest
// satisfy the interface with zero-value returns.
type fakeSSLCertRepo struct {
	byDomain map[string]*models.SSLCertificate
	revoked  map[string]bool
}

func newFakeSSLCertRepo() *fakeSSLCertRepo {
	return &fakeSSLCertRepo{byDomain: map[string]*models.SSLCertificate{}, revoked: map[string]bool{}}
}

func (f *fakeSSLCertRepo) Create(context.Context, *models.SSLCertificate) error { return nil }
func (f *fakeSSLCertRepo) FindByDomainID(_ context.Context, domainID string) (*models.SSLCertificate, error) {
	c, ok := f.byDomain[domainID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return c, nil
}
func (f *fakeSSLCertRepo) FindByDomainIDs(context.Context, []string) ([]models.SSLCertificate, error) {
	return nil, nil
}
func (f *fakeSSLCertRepo) UpdateStatus(context.Context, string, string, *string) error { return nil }
func (f *fakeSSLCertRepo) UpdateAfterIssuance(context.Context, string, time.Time, time.Time, string, string) error {
	return nil
}
func (f *fakeSSLCertRepo) UpdateAfterRenewal(context.Context, string, time.Time, time.Time, string, string) error {
	return nil
}
func (f *fakeSSLCertRepo) MarkRevoked(_ context.Context, id string) error {
	f.revoked[id] = true
	return nil
}
func (f *fakeSSLCertRepo) DeleteByDomainID(context.Context, string) error { return nil }
func (f *fakeSSLCertRepo) ListAll(context.Context) ([]repository.SSLCertificateWithDomain, error) {
	return nil, nil
}
func (f *fakeSSLCertRepo) ListByUserID(context.Context, string) ([]repository.SSLCertificateWithDomain, error) {
	return nil, nil
}
func (f *fakeSSLCertRepo) UpdateSelfSigned(context.Context, string, string, string, time.Time) error {
	return nil
}

func (f *fakeSSLCertRepo) UpdateCustom(context.Context, string, string, string, time.Time) error {
	return nil
}
func (f *fakeSSLCertRepo) UpdateAfterACMEFailure(context.Context, string, string, time.Time, int, *string, *string, *time.Time) error {
	return nil
}
func (f *fakeSSLCertRepo) UpdateAfterACMEFailureCapped(context.Context, string, string, int, *string, *string, *time.Time) error {
	return nil
}
func (f *fakeSSLCertRepo) MarkFailed(context.Context, string, string) error { return nil }
func (f *fakeSSLCertRepo) ListDueForACMERetry(context.Context, time.Time, int) ([]models.SSLCertificate, error) {
	return nil, nil
}

func newFakeDomainRepo() *fakeDomainRepo {
	return &fakeDomainRepo{domains: map[string]*models.Domain{}}
}

// RefreshObservedExpiry — JAB-203 observation pass; no behaviour needed here.
func (f *fakeSSLCertRepo) RefreshObservedExpiry(context.Context, string, time.Time, time.Time) error {
	return nil
}
