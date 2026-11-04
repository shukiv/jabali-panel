package reconciler

import (
	"context"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/services"
)

// `jabali panel-cert issue` promises an issuance on the next pass (≤60s). The
// CLI used to write a fresh pending_acme, which this reconciler reads as its
// own dispatch in flight: the issuance waited out the 10-minute lock.

func issueQueueReconciler() (*Reconciler, *fakeAgent) {
	fa := &fakeAgent{}
	return &Reconciler{
		agent:                fa,
		log:                  selfSignTestLogger(),
		panelCerts:           &fakePanelCertRepo{rows: map[string]*models.PanelCertificate{}},
		panelCertRoutability: &services.PanelCertRoutability{Resolver: swResolver{"host.example.com": swIP}},
	}, fa
}

func issuedHostnameRow() *models.PanelCertificate {
	return &models.PanelCertificate{
		Kind: models.PanelCertKindHostname, Hostname: "host.example.com", UseLE: true,
		Status: models.PanelCertStatusIssued, UpdatedAt: time.Now(),
	}
}

func TestPanelCertQueueIssue_DispatchesOnTheNextPass(t *testing.T) {
	r, fa := issueQueueReconciler()
	row := issuedHostnameRow()
	row.QueueIssue(time.Now())

	r.reconcileOnePanelCert(context.Background(), models.PanelCertKindHostname, row, "admin@example.com", swIP)

	if n, _ := countCalls(fa, "ssl.panel.issue"); n != 1 {
		t.Fatalf("ssl.panel.issue dispatched %d times on the first pass after `panel-cert issue`, want 1", n)
	}
}

// The lock itself stays: a pending_acme this reconciler just wrote means a
// dispatch is in flight, and a second one would double-issue.
func TestPanelCertFreshPendingACME_IsLeftToTheDispatchInFlight(t *testing.T) {
	r, fa := issueQueueReconciler()
	row := issuedHostnameRow()
	row.Status = models.PanelCertStatusPendingACME

	r.reconcileOnePanelCert(context.Background(), models.PanelCertKindHostname, row, "admin@example.com", swIP)

	if n, _ := countCalls(fa, "ssl.panel.issue"); n != 0 {
		t.Fatalf("dispatched %d times over an issuance in flight", n)
	}
}
