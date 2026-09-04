package egressops

import (
	"context"
	"encoding/json"
	"errors"
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

// --- SetPolicy / ValidateDestination contract (JAB-341) --------------------
//
// These lock the canonical accept/reject matrix that both adapters share
// (PUT /users/:id/egress and the CLI per-user-egress set-policy). A regression
// in either adapter now fails here, not silently in production nft rendering.

func iptr(v int) *int       { return &v }
func sptr(s string) *string { return &s }

func TestValidState(t *testing.T) {
	for _, s := range []string{"off", "learning", "enforced"} {
		if !ValidState(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", "ENFORCED", "on", "block"} {
		if ValidState(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestValidateDestination_Matrix(t *testing.T) {
	ok := []models.EgressDestination{
		{CIDR: "10.0.0.0/8"},
		{CIDR: "1.2.3.4/32", Port: iptr(443)},
		{CIDR: "1.2.3.4/32", Port: iptr(53), Protocol: "udp"},
		{CIDR: "1.2.3.4/32", Comment: "ok comment"},
	}
	for _, d := range ok {
		dd := d
		if err := ValidateDestination(&dd); err != nil {
			t.Errorf("%+v: unexpected error %v", d, err)
		}
	}

	bad := []models.EgressDestination{
		{CIDR: "not-a-cidr"},
		{CIDR: "1.2.3.4"},                         // no mask
		{CIDR: "1.2.3.4/32", Port: iptr(0)},       // port 0
		{CIDR: "1.2.3.4/32", Port: iptr(99999)},   // port out of range
		{CIDR: "1.2.3.4/32", Protocol: "sctp"},    // unknown protocol
		{CIDR: "1.2.3.4/32", Comment: "a\nnft x"}, // newline injection
		{CIDR: "1.2.3.4/32", Comment: "a\rb"},     // carriage return
		{CIDR: "1.2.3.4/32", Comment: "a\x00b"},   // NUL
		{CIDR: "1.2.3.4/32", Comment: "a\tb"},     // control char (tab)
	}
	for _, d := range bad {
		dd := d
		if err := ValidateDestination(&dd); err == nil {
			t.Errorf("%+v: expected error, got nil", d)
		}
	}
}

func TestValidateDestination_DefaultsProtocol(t *testing.T) {
	d := models.EgressDestination{CIDR: "1.2.3.4/32"}
	if err := ValidateDestination(&d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Protocol != models.UserEgressProtocolTCP {
		t.Errorf("empty protocol should default to tcp, got %q", d.Protocol)
	}
}

func TestSetPolicy_PersistsOnceAndCanonicalizes(t *testing.T) {
	pols := &fakePolicyRepo{}
	err := SetPolicy(context.Background(), pols, SetPolicyInput{
		UserID: "U1",
		State:  models.UserEgressStateEnforced,
		AllowedExtra: []models.EgressDestination{
			{CIDR: "1.2.3.4/32", Port: iptr(443)}, // proto omitted -> tcp
		},
		UpdatedBy: sptr("admin1"),
	})
	if err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	// AC5: exactly one policy write scheduled.
	if pols.upsertCalls != 1 {
		t.Fatalf("expected exactly one upsert, got %d", pols.upsertCalls)
	}
	if pols.upserted.State != models.UserEgressStateEnforced {
		t.Errorf("state = %q", pols.upserted.State)
	}
	// AC4: provenance recorded.
	if pols.upserted.UpdatedBy == nil || *pols.upserted.UpdatedBy != "admin1" {
		t.Errorf("updated_by = %v, want admin1", pols.upserted.UpdatedBy)
	}
	var extras []models.EgressDestination
	if err := json.Unmarshal(pols.upserted.AllowedExtra, &extras); err != nil {
		t.Fatalf("decode allowed_extra: %v", err)
	}
	if len(extras) != 1 || extras[0].Protocol != models.UserEgressProtocolTCP {
		t.Errorf("destination not canonicalized: %+v", extras)
	}
}

func TestSetPolicy_CLIProvenanceNil(t *testing.T) {
	pols := &fakePolicyRepo{}
	if err := SetPolicy(context.Background(), pols, SetPolicyInput{
		UserID: "U1", State: models.UserEgressStateOff, UpdatedBy: nil,
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	if pols.upserted.UpdatedBy != nil {
		t.Errorf("CLI actor should leave updated_by nil, got %v", *pols.upserted.UpdatedBy)
	}
	// Empty list must serialize as [] (not null) so the not-null column is clean.
	if string(pols.upserted.AllowedExtra) != "[]" {
		t.Errorf("empty allow list = %q, want []", pols.upserted.AllowedExtra)
	}
}

func TestSetPolicy_RejectsInvalidState(t *testing.T) {
	pols := &fakePolicyRepo{}
	err := SetPolicy(context.Background(), pols, SetPolicyInput{UserID: "U1", State: "block"})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState, got %v", err)
	}
	if pols.upsertCalls != 0 {
		t.Errorf("invalid state must not persist, got %d upserts", pols.upsertCalls)
	}
}

func TestSetPolicy_RejectsTooManyExtras(t *testing.T) {
	pols := &fakePolicyRepo{}
	extras := make([]models.EgressDestination, MaxAllowedExtras+1)
	for i := range extras {
		extras[i] = models.EgressDestination{CIDR: "1.2.3.4/32"}
	}
	err := SetPolicy(context.Background(), pols, SetPolicyInput{
		UserID: "U1", State: models.UserEgressStateEnforced, AllowedExtra: extras,
	})
	if !errors.Is(err, ErrTooManyExtras) {
		t.Fatalf("expected ErrTooManyExtras, got %v", err)
	}
	if pols.upsertCalls != 0 {
		t.Errorf("over-limit list must not persist, got %d upserts", pols.upsertCalls)
	}
}

func TestSetPolicy_RejectsBadDestinationWithIndex(t *testing.T) {
	pols := &fakePolicyRepo{}
	err := SetPolicy(context.Background(), pols, SetPolicyInput{
		UserID: "U1", State: models.UserEgressStateEnforced,
		AllowedExtra: []models.EgressDestination{
			{CIDR: "1.2.3.4/32"},                      // index 0 ok
			{CIDR: "1.2.3.4/32", Comment: "x\nnft y"}, // index 1 injection
		},
	})
	var de *DestinationError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DestinationError, got %v", err)
	}
	if de.Index != 1 {
		t.Errorf("DestinationError.Index = %d, want 1", de.Index)
	}
	if pols.upsertCalls != 0 {
		t.Errorf("unsafe destination must not persist, got %d upserts", pols.upsertCalls)
	}
}
