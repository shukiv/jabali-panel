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
	// JAB-235: records SetIssueMethod calls (cert id → method)
	issueMethods map[string]string
	// JAB-224 ssl_resurrect
	exhausted    []models.SSLCertificate
	exhaustedErr error
	rearmErr     error
	rearmed      map[string]int
	// GH #887: counts ACME-failure recordings so a test can assert a
	// webroot_not_ready deferral records NO failure (quiet skip).
	acmeFailures int
}

func newFakeSSLCertRepo() *fakeSSLCertRepo {
	return &fakeSSLCertRepo{
		byDomain:     map[string]*models.SSLCertificate{},
		revoked:      map[string]bool{},
		issueMethods: map[string]string{},
	}
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
func (f *fakeSSLCertRepo) SetIssueMethod(_ context.Context, id, method string) error {
	if f.issueMethods != nil {
		f.issueMethods[id] = method
	}
	return nil
}

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
func (f *fakeSSLCertRepo) UpdateAfterACMEFailure(_ context.Context, id, lastError string, nextRetryAt time.Time, retryCount int, _, _ *string, _ *time.Time) error {
	f.acmeFailures++
	// Record onto the row so tests can assert what the reconciler wrote —
	// the DNS pre-flight tests (GH #896) check the reason, the retry count,
	// and the next-retry time.
	for _, c := range f.byDomain {
		if c.ID == id {
			le := lastError
			nr := nextRetryAt
			c.LastError = &le
			c.NextRetryAt = &nr
			c.RetryCount = retryCount
		}
	}
	return nil
}
func (f *fakeSSLCertRepo) UpdateAfterACMEFailureCapped(context.Context, string, string, int, *string, *string, *time.Time) error {
	f.acmeFailures++
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

// JAB-224 (ssl_resurrect): exhausted certificates re-armed for another attempt.
func (f *fakeSSLCertRepo) ListExhaustedForSSLEnabledDomains(_ context.Context, before time.Time, limit int) ([]models.SSLCertificate, error) {
	if f.exhaustedErr != nil {
		return nil, f.exhaustedErr
	}
	var out []models.SSLCertificate
	for _, c := range f.exhausted {
		if c.LastAttemptAt != nil && c.LastAttemptAt.After(before) {
			continue // still resting
		}
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeSSLCertRepo) RearmACME(_ context.Context, id string, retryCount int, now time.Time) error {
	if f.rearmErr != nil {
		return f.rearmErr
	}
	if f.rearmed == nil {
		f.rearmed = map[string]int{}
	}
	f.rearmed[id] = retryCount
	return nil
}
