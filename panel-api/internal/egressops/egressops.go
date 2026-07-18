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
	"os"
	"strings"
	"time"

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


// DefaultPinPath is the operator pin file for the per-user egress LEARNING
// hold. When it contains the literal "learning", FlipMature is a no-op — an
// operator-controlled hold for hosts where the soak needs to run longer.
// Shared by the CLI (per-user-egress flip-mature) and the admin API so the two
// paths honor the same pin (verify_wire_contract scar).
const DefaultPinPath = "/etc/jabali/per-user-egress.mode"

// ReadEgressPin reports whether the operator pin at path holds the LEARNING
// hold and returns the trimmed file contents for display. A missing/unreadable
// file is "not pinned" (the common case).
func ReadEgressPin(path string) (pinned bool, value string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}
	value = strings.TrimSpace(string(b))
	return value == models.UserEgressStateLearning, value
}

// FlipMatureResult is the outcome of a FlipMature run — the same shape whether
// dry-run (preview) or a real promotion, so the CLI and the admin GUI render
// from one struct.
type FlipMatureResult struct {
	SoakDays int                        // soak window applied (days)
	DryRun   bool                       // true = preview only, nothing written
	Pinned   bool                       // operator pin active → no rows flipped
	PinValue string                     // trimmed pin file contents, for display
	Eligible []models.UserEgressPolicy  // mature LEARNING rows (would flip / did flip)
	Flipped  []string                   // user IDs actually flipped (empty on dry-run/pin)
	Failed   map[string]string          // user ID → upsert error, for the rows that failed
}

// FlipMature finds user_egress_policies rows in LEARNING older than soakDays and
// flips them to ENFORCED — the graduate-from-soak decision. Honors the operator
// pin at pinPath (no-op when "learning"). With dryRun it lists eligible rows
// without writing. Errors only on bad input or the mature-list query; per-row
// upsert failures are collected in Failed so one bad row doesn't abort the rest.
func FlipMature(ctx context.Context, policies repository.UserEgressPolicyRepository, soakDays int, pinPath string, dryRun bool) (*FlipMatureResult, error) {
	if soakDays <= 0 {
		return nil, errors.New("soak-days must be > 0")
	}
	res := &FlipMatureResult{SoakDays: soakDays, DryRun: dryRun, Failed: map[string]string{}}

	if pinned, val := ReadEgressPin(pinPath); pinned {
		res.Pinned = true
		res.PinValue = val
		return res, nil
	}

	soak := time.Duration(soakDays) * 24 * time.Hour
	rows, err := policies.ListMatureLearning(ctx, soak)
	if err != nil {
		return nil, err
	}
	res.Eligible = rows
	if dryRun {
		return res, nil
	}

	for _, r := range rows {
		next := r
		next.State = models.UserEgressStateEnforced
		if err := policies.Upsert(ctx, &next); err != nil {
			res.Failed[r.UserID] = err.Error()
			continue
		}
		res.Flipped = append(res.Flipped, r.UserID)
	}
	return res, nil
}
