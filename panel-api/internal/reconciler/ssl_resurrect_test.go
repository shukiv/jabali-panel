package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func resurrectReconciler(f *fakeSSLCertRepo) *Reconciler {
	return &Reconciler{
		sslCerts: f,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func exhaustedCert(id string, lastAttempt time.Time) models.SSLCertificate {
	return models.SSLCertificate{
		ID:            id,
		Status:        models.SSLStatusFailed,
		RetryCount:    20,
		LastAttemptAt: &lastAttempt,
	}
}

// The whole point: a certificate that gave up while SSL is still wanted gets
// another chance, so cutover is picked up without operator action.
func TestReconcileSSLResurrect_RearmsExhaustedCert(t *testing.T) {
	f := newFakeSSLCertRepo()
	f.exhausted = []models.SSLCertificate{exhaustedCert("c1", time.Now().UTC().Add(-3*time.Hour))}

	resurrectReconciler(f).ReconcileSSLResurrect(context.Background())

	if len(f.rearmed) != 1 {
		t.Fatalf("want 1 re-armed, got %d", len(f.rearmed))
	}
	// Re-arm must NOT zero the counter — that would hand out a full fresh
	// budget every hour and could breach Let's Encrypt's failed-validation
	// limits when a whole migration batch is pre-cutover and failing together.
	got := f.rearmed["c1"]
	if got == 0 {
		t.Error("retry counter was zeroed — grants a full budget every cycle")
	}
	if want := sslResurrectMaxRetryCount - sslResurrectAttempts; got != want {
		t.Errorf("retry_count = %d, want %d (i.e. %d attempts)", got, want, sslResurrectAttempts)
	}
}

// Rate limiting: a cert that failed moments ago must rest, or the pass would
// retry hot every tick.
func TestReconcileSSLResurrect_RespectsRestInterval(t *testing.T) {
	f := newFakeSSLCertRepo()
	f.exhausted = []models.SSLCertificate{exhaustedCert("recent", time.Now().UTC().Add(-2*time.Minute))}

	resurrectReconciler(f).ReconcileSSLResurrect(context.Background())

	if len(f.rearmed) != 0 {
		t.Errorf("re-armed a certificate that failed 2 minutes ago: %v", f.rearmed)
	}
}

// One tick must not queue a whole fleet's worth of ACME work.
func TestReconcileSSLResurrect_BatchCapped(t *testing.T) {
	f := newFakeSSLCertRepo()
	old := time.Now().UTC().Add(-6 * time.Hour)
	for i := 0; i < sslResurrectBatch*3; i++ {
		f.exhausted = append(f.exhausted, exhaustedCert(string(rune('a'+i%26))+string(rune('a'+i/26)), old))
	}

	resurrectReconciler(f).ReconcileSSLResurrect(context.Background())

	if len(f.rearmed) > sslResurrectBatch {
		t.Errorf("re-armed %d in one pass, cap is %d", len(f.rearmed), sslResurrectBatch)
	}
}

// Best-effort: a repository error must not take the reconciler tick down.
func TestReconcileSSLResurrect_SurvivesRepoErrors(t *testing.T) {
	f := newFakeSSLCertRepo()
	f.exhaustedErr = errors.New("db down")
	resurrectReconciler(f).ReconcileSSLResurrect(context.Background()) // must not panic

	f2 := newFakeSSLCertRepo()
	f2.exhausted = []models.SSLCertificate{exhaustedCert("c1", time.Now().UTC().Add(-3*time.Hour))}
	f2.rearmErr = errors.New("write failed")
	resurrectReconciler(f2).ReconcileSSLResurrect(context.Background())
	if len(f2.rearmed) != 0 {
		t.Error("counted a re-arm that failed to write")
	}
}

// A settled host has nothing to do and must stay quiet.
func TestReconcileSSLResurrect_NoopWhenNothingExhausted(t *testing.T) {
	f := newFakeSSLCertRepo()
	resurrectReconciler(f).ReconcileSSLResurrect(context.Background())
	if len(f.rearmed) != 0 {
		t.Errorf("re-armed something on a clean host: %v", f.rearmed)
	}
}

// The attempt budget must stay inside Let's Encrypt's failed-validation limit
// (5 per hostname per hour). Pin the arithmetic so a future tweak to either
// constant cannot quietly exceed it.
func TestSSLResurrectStaysUnderLetsEncryptFailureLimit(t *testing.T) {
	const leFailedValidationsPerHour = 5
	perHour := float64(sslResurrectAttempts) * (float64(time.Hour) / float64(sslResurrectAfter))
	if perHour > leFailedValidationsPerHour {
		t.Errorf("re-arm grants %.1f failed validations/hour, Let's Encrypt allows %d",
			perHour, leFailedValidationsPerHour)
	}
}
