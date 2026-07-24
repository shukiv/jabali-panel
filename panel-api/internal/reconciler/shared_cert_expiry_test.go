package reconciler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeExpiryRepo embeds the interface (rest nil) and only implements what the
// expiry check calls.
type fakeExpiryRepo struct {
	repository.SharedCertificateRepository
	expiring []models.SharedCertificate
	attached map[string]int64
}

func (r *fakeExpiryRepo) ListExpiring(_ context.Context, _ time.Time) ([]models.SharedCertificate, error) {
	return r.expiring, nil
}
func (r *fakeExpiryRepo) CountAttachedDomains(_ context.Context, id string) (int64, error) {
	return r.attached[id], nil
}

// JAB-170 phase 7: expired + soon-to-expire shared certs each warn (distinct
// message), once per boot.
func TestCheckSharedCertExpiry_Warns(t *testing.T) {
	cap := &captureHandler{}
	past := time.Now().Add(-24 * time.Hour)
	soon := time.Now().Add(3 * 24 * time.Hour)
	repo := &fakeExpiryRepo{
		expiring: []models.SharedCertificate{
			{ID: "C1", Name: "expired", ExpiresAt: &past},
			{ID: "C2", Name: "soon", ExpiresAt: &soon},
		},
		attached: map[string]int64{"C1": 3, "C2": 1},
	}
	r := &Reconciler{sharedCerts: repo, log: slog.New(cap)}

	r.checkSharedCertExpiry(context.Background())
	if !cap.warnMsgContains("EXPIRED") {
		t.Error("expected an EXPIRED warning for the past-dated cert")
	}
	if !cap.warnMsgContains("expiring within") {
		t.Error("expected an expiring-soon warning")
	}

	// once per boot: a second call emits nothing more.
	before := len(cap.records)
	r.checkSharedCertExpiry(context.Background())
	if len(cap.records) != before {
		t.Errorf("second check should be a no-op (once per boot)")
	}
}

// Nil repo is a safe no-op.
func TestCheckSharedCertExpiry_NilRepo(t *testing.T) {
	cap := &captureHandler{}
	r := &Reconciler{log: slog.New(cap)}
	r.checkSharedCertExpiry(context.Background())
	if len(cap.records) != 0 {
		t.Error("nil repo should not warn")
	}
}
