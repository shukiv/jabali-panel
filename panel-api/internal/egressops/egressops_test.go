package egressops

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes -----------------------------------------------------------------

type fakeReqRepo struct {
	req         *models.UserEgressRequest
	getErr      error
	decideErr   error
	decidedWith [3]string // id, status, reviewedBy
	decideCalls int
}

func (f *fakeReqRepo) Get(_ context.Context, _ string) (*models.UserEgressRequest, error) {
	return f.req, f.getErr
}
func (f *fakeReqRepo) Decide(_ context.Context, id, status, reviewedBy string) error {
	f.decideCalls++
	f.decidedWith = [3]string{id, status, reviewedBy}
	if f.req != nil && f.decideErr == nil {
		f.req.Status = status
	}
	return f.decideErr
}
func (f *fakeReqRepo) Create(context.Context, *models.UserEgressRequest) error { return nil }
func (f *fakeReqRepo) ListPending(context.Context) ([]models.UserEgressRequest, error) {
	return nil, nil
}
func (f *fakeReqRepo) ListByUser(context.Context, string) ([]models.UserEgressRequest, error) {
	return nil, nil
}
func (f *fakeReqRepo) CancelPending(context.Context, string, string) error { return nil }

type fakePolicyRepo struct {
	policy      *models.UserEgressPolicy
	getErr      error
	upserted    *models.UserEgressPolicy
	upsertCalls int
	mature      []models.UserEgressPolicy // JAB-13: rows ListMatureLearning returns
}

func (f *fakePolicyRepo) Get(context.Context, string) (*models.UserEgressPolicy, error) {
	return f.policy, f.getErr
}
func (f *fakePolicyRepo) Upsert(_ context.Context, p *models.UserEgressPolicy) error {
	f.upsertCalls++
	f.upserted = p
	return nil
}
func (f *fakePolicyRepo) EnsureDefault(context.Context, string, string) error { return nil }
func (f *fakePolicyRepo) List(context.Context) ([]models.UserEgressPolicy, error) {
	return nil, nil
}
func (f *fakePolicyRepo) ListAllForReconcile(context.Context) ([]repository.PolicyForReconcile, error) {
	return nil, nil
}
func (f *fakePolicyRepo) SetDropCount(context.Context, string, uint64, time.Time) error { return nil }
func (f *fakePolicyRepo) StateCounts(context.Context) (map[string]uint, error)          { return nil, nil }
func (f *fakePolicyRepo) ListMatureLearning(context.Context, time.Duration) ([]models.UserEgressPolicy, error) {
	return f.mature, nil
}

// --- tests -----------------------------------------------------------------

func port(p uint) *uint { return &p }

func TestDecideRequest_ApproveFoldsIntoNewPolicy(t *testing.T) {
	reqs := &fakeReqRepo{req: &models.UserEgressRequest{
		ID: "REQ1", UserID: "U1", CIDR: "1.2.3.4/32", Port: port(443), Protocol: "tcp",
		Status: models.UserEgressRequestStatusPending,
	}}
	pols := &fakePolicyRepo{getErr: repository.ErrNotFound} // no policy yet

	got, err := DecideRequest(context.Background(), Deps{reqs, pols},
		"REQ1", models.UserEgressRequestStatusApproved, "admin1")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got.Status != models.UserEgressRequestStatusApproved {
		t.Errorf("request status = %q", got.Status)
	}
	if reqs.decideCalls != 1 || reqs.decidedWith[1] != models.UserEgressRequestStatusApproved {
		t.Errorf("Decide not called with approved: %+v", reqs.decidedWith)
	}
	if pols.upsertCalls != 1 {
		t.Fatalf("expected policy upsert, got %d calls", pols.upsertCalls)
	}
	if pols.upserted.State != models.UserEgressStateEnforced {
		t.Errorf("new policy should be enforced, got %q", pols.upserted.State)
	}
	var extras []models.EgressDestination
	_ = json.Unmarshal(pols.upserted.AllowedExtra, &extras)
	if len(extras) != 1 || extras[0].CIDR != "1.2.3.4/32" || extras[0].Port == nil || *extras[0].Port != 443 {
		t.Errorf("folded destination wrong: %+v", extras)
	}
}

func TestDecideRequest_ApproveDedupesExisting(t *testing.T) {
	existing, _ := json.Marshal([]models.EgressDestination{
		{CIDR: "1.2.3.4/32", Port: func() *int { v := 443; return &v }(), Protocol: "tcp"},
	})
	reqs := &fakeReqRepo{req: &models.UserEgressRequest{
		ID: "REQ2", UserID: "U1", CIDR: "1.2.3.4/32", Port: port(443), Protocol: "tcp",
		Status: models.UserEgressRequestStatusPending,
	}}
	pols := &fakePolicyRepo{policy: &models.UserEgressPolicy{
		UserID: "U1", State: models.UserEgressStateEnforced, AllowedExtra: existing,
	}}

	if _, err := DecideRequest(context.Background(), Deps{reqs, pols},
		"REQ2", models.UserEgressRequestStatusApproved, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// Destination already present → no upsert.
	if pols.upsertCalls != 0 {
		t.Errorf("expected no upsert for duplicate destination, got %d", pols.upsertCalls)
	}
}

func TestDecideRequest_DenyDoesNotFold(t *testing.T) {
	reqs := &fakeReqRepo{req: &models.UserEgressRequest{
		ID: "REQ3", UserID: "U1", CIDR: "1.2.3.4/32", Status: models.UserEgressRequestStatusPending,
	}}
	pols := &fakePolicyRepo{getErr: repository.ErrNotFound}

	if _, err := DecideRequest(context.Background(), Deps{reqs, pols},
		"REQ3", models.UserEgressRequestStatusDenied, ""); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if reqs.decidedWith[1] != models.UserEgressRequestStatusDenied {
		t.Errorf("Decide status = %q", reqs.decidedWith[1])
	}
	if pols.upsertCalls != 0 {
		t.Errorf("deny must not touch policy, got %d upserts", pols.upsertCalls)
	}
}

func TestDecideRequest_AlreadyDecided(t *testing.T) {
	reqs := &fakeReqRepo{req: &models.UserEgressRequest{
		ID: "REQ4", UserID: "U1", Status: models.UserEgressRequestStatusApproved,
	}}
	pols := &fakePolicyRepo{}

	_, err := DecideRequest(context.Background(), Deps{reqs, pols},
		"REQ4", models.UserEgressRequestStatusApproved, "")
	if err != ErrAlreadyDecided {
		t.Fatalf("expected ErrAlreadyDecided, got %v", err)
	}
	if reqs.decideCalls != 0 {
		t.Errorf("Decide should not run on a non-pending request")
	}
}

func TestDecideRequest_NotFound(t *testing.T) {
	reqs := &fakeReqRepo{getErr: repository.ErrNotFound}
	_, err := DecideRequest(context.Background(), Deps{reqs, &fakePolicyRepo{}},
		"NOPE", models.UserEgressRequestStatusApproved, "")
	if err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
