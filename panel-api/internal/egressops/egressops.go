// Package egressops holds the per-user PHP-FPM egress-firewall (M34)
// application logic shared by the HTTP handler (internal/api/user_egress.go)
// and the CLI (cmd/server/per_user_egress_cmd.go). Extracting the
// approve/deny + fold-into-policy cascade here keeps the web and headless
// paths byte-identical so the two can never drift (verify_wire_contract scar).
package egressops

import (
	"context"
	"encoding/json"
	"errors"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ErrAlreadyDecided is returned by DecideRequest when the target request is no
// longer pending. Callers map it to a 409 (web) or a friendly message (CLI).
var ErrAlreadyDecided = errors.New("egress request already decided")

// Deps are the repositories DecideRequest operates on.
type Deps struct {
	Requests repository.UserEgressRequestRepository
	Policies repository.UserEgressPolicyRepository
}

// DecideRequest approves or denies a pending egress request. On approval it
// folds the request's destination into the user's policy (dedup on
// cidr+port+protocol, creating an enforced policy when none exists). reviewedBy
// is the deciding actor's user id ("" if unknown, e.g. the CLI). It returns the
// request row even when already decided, so callers can report its status.
//
// Errors: repository.ErrNotFound (no such request), ErrAlreadyDecided (not
// pending), or any repository failure from Decide/Upsert.
func DecideRequest(ctx context.Context, deps Deps, requestID, status, reviewedBy string) (*models.UserEgressRequest, error) {
	req, err := deps.Requests.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if req.Status != models.UserEgressRequestStatusPending {
		return req, ErrAlreadyDecided
	}
	if err := deps.Requests.Decide(ctx, requestID, status, reviewedBy); err != nil {
		return req, err
	}
	if status == models.UserEgressRequestStatusApproved {
		if err := foldRequestIntoPolicy(ctx, deps, req, reviewedBy); err != nil {
			return req, err
		}
	}
	return req, nil
}

// foldRequestIntoPolicy appends the approved destination to the user's
// allowed_extra list. Dedupes on (cidr, port, protocol). Creates an enforced
// policy when the user has none yet.
func foldRequestIntoPolicy(ctx context.Context, deps Deps, req *models.UserEgressRequest, reviewedBy string) error {
	policy, err := deps.Policies.Get(ctx, req.UserID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return err
		}
		policy = &models.UserEgressPolicy{
			UserID: req.UserID,
			State:  models.UserEgressStateEnforced,
		}
	}
	existing, _ := policy.DecodeAllowedExtra()
	proto := req.Protocol
	if proto == "" {
		proto = models.UserEgressProtocolTCP
	}
	for _, e := range existing {
		samePort := (e.Port == nil && req.Port == nil) ||
			(e.Port != nil && req.Port != nil && uint(*e.Port) == *req.Port)
		if e.CIDR == req.CIDR && samePort && e.Protocol == proto {
			return nil // already present
		}
	}
	var portPtr *int
	if req.Port != nil {
		v := int(*req.Port)
		portPtr = &v
	}
	existing = append(existing, models.EgressDestination{
		CIDR:     req.CIDR,
		Port:     portPtr,
		Protocol: proto,
		Comment:  "approved request " + req.ID,
	})
	jsonExtras, _ := json.Marshal(existing)
	policy.AllowedExtra = jsonExtras
	if policy.State == "" {
		policy.State = models.UserEgressStateEnforced
	}
	if reviewedBy != "" {
		policy.UpdatedBy = &reviewedBy
	}
	return deps.Policies.Upsert(ctx, policy)
}
