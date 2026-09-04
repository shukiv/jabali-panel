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
	"fmt"
	"net"
	"os"
	"strings"
	"time"
	"unicode"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ErrAlreadyDecided is returned by DecideRequest when the target request is no
// longer pending. Callers map it to a 409 (web) or a friendly message (CLI).
var ErrAlreadyDecided = errors.New("egress request already decided")

// MaxAllowedExtras caps the per-user override list size. Hard ceiling rather
// than a per-row vs per-user limit — the nft renderer emits one rule per entry,
// and a runaway list would inflate the rule file without bound. This is the ONE
// definition of the cap; both the HTTP handler (PUT /users/:id/egress) and the
// CLI (per-user-egress set-policy) enforce it through SetPolicy.
const MaxAllowedExtras = 50

// Egress set-policy validation errors. Adapters map these onto their own wire
// (HTTP JSON error codes; CLI messages) with errors.Is / errors.As — the same
// pattern ErrAlreadyDecided already uses, so the canonical accept/reject matrix
// lives here and cannot drift between the two adapters.
var (
	// ErrInvalidState — State is not one of off|learning|enforced.
	ErrInvalidState = errors.New("egress state must be off, learning, or enforced")
	// ErrTooManyExtras — the allow list exceeds MaxAllowedExtras.
	ErrTooManyExtras = errors.New("too many allowed destinations")
)

// DestinationError wraps a per-entry ValidateDestination failure with the
// entry's index, so an adapter can tell the caller which allowed destination
// was rejected (HTTP surfaces the index in the JSON body; the CLI names the
// --allow position).
type DestinationError struct {
	Index int
	Err   error
}

func (e *DestinationError) Error() string {
	return fmt.Sprintf("allowed_extra[%d]: %v", e.Index, e.Err)
}
func (e *DestinationError) Unwrap() error { return e.Err }

// ValidState reports whether s is one of the three egress states.
func ValidState(s string) bool {
	return s == models.UserEgressStateOff ||
		s == models.UserEgressStateLearning ||
		s == models.UserEgressStateEnforced
}

// ValidateDestination canonicalizes and validates one allowed-destination entry
// in place. It defaults an empty Protocol to tcp — the single place that default
// now lives — and rejects a bad CIDR, an out-of-range port, an unknown
// protocol, an over-long comment, or a comment carrying a newline / control
// character.
//
// The comment safety check is security-critical: the comment is rendered into
// /etc/nftables.d/jabali-per-user-egress.nft, which is loaded by `nft -f` as
// root. A newline would inject arbitrary nftables lines; a control character can
// make the file unparseable, which fails the whole ruleset closed-to-absent (the
// egress policy silently stops applying box-wide). The agent sanitises again at
// render time — this is the boundary half of that pair. Both adapters reach nft
// rendering only through a policy this function has cleared.
func ValidateDestination(d *models.EgressDestination) error {
	if _, _, err := net.ParseCIDR(d.CIDR); err != nil {
		return errors.New("cidr: " + err.Error())
	}
	if d.Port != nil && (*d.Port < 1 || *d.Port > 65535) {
		return errors.New("port out of range")
	}
	if d.Protocol == "" {
		d.Protocol = models.UserEgressProtocolTCP
	}
	if d.Protocol != models.UserEgressProtocolTCP && d.Protocol != models.UserEgressProtocolUDP {
		return errors.New("protocol must be tcp or udp")
	}
	if len(d.Comment) > 200 {
		return errors.New("comment too long")
	}
	if i := strings.IndexFunc(d.Comment, func(r rune) bool {
		return r == '\n' || r == '\r' || r == 0 || unicode.IsControl(r)
	}); i >= 0 {
		return errors.New("comment must not contain newlines or control characters")
	}
	return nil
}

// SetPolicyInput is the canonical set-policy request shared by both adapters.
// AllowedExtra REPLACES the user's existing allow list. UpdatedBy is the acting
// user's id, or nil when the actor has no user identity — the operator CLI has
// no interactive session, so it passes nil and the CLI audit row carries that
// provenance instead. SetPolicy mutates the AllowedExtra entries in place to
// canonicalize each one (protocol default).
type SetPolicyInput struct {
	UserID       string
	State        string
	AllowedExtra []models.EgressDestination
	UpdatedBy    *string
}

// SetPolicy validates and persists a user's egress policy — the one
// authoritative mutation both the HTTP handler and the CLI call, so the two can
// never diverge (verify_wire_contract scar). It enforces the canonical matrix
// (ValidState, MaxAllowedExtras, per-destination ValidateDestination) and
// Upserts exactly once. Convergence stays tick-based by existing design: the
// running reconciler re-renders nftables on its next tick — there is no per-user
// immediate-apply handle, and this call schedules exactly one policy write, not
// a separate dispatch.
//
// Errors: ErrInvalidState, ErrTooManyExtras, *DestinationError (with the failing
// entry's Index), or a repository error from Upsert.
func SetPolicy(ctx context.Context, policies repository.UserEgressPolicyRepository, in SetPolicyInput) error {
	if !ValidState(in.State) {
		return ErrInvalidState
	}
	if len(in.AllowedExtra) > MaxAllowedExtras {
		return ErrTooManyExtras
	}
	for i := range in.AllowedExtra {
		if err := ValidateDestination(&in.AllowedExtra[i]); err != nil {
			return &DestinationError{Index: i, Err: err}
		}
	}
	extras := in.AllowedExtra
	if extras == nil {
		extras = []models.EgressDestination{}
	}
	jsonExtras, err := json.Marshal(extras)
	if err != nil {
		return err
	}
	return policies.Upsert(ctx, &models.UserEgressPolicy{
		UserID:       in.UserID,
		State:        in.State,
		AllowedExtra: jsonExtras,
		UpdatedBy:    in.UpdatedBy,
	})
}

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
	var portPtr *int
	if req.Port != nil {
		v := int(*req.Port)
		portPtr = &v
	}
	dest := models.EgressDestination{
		CIDR:     req.CIDR,
		Port:     portPtr,
		Protocol: req.Protocol, // ValidateDestination defaults an empty proto to tcp
		Comment:  "approved request " + req.ID,
	}
	// The request was validated at submission time; re-run the canonical
	// validator so the proto default comes from one place and a somehow-bad
	// row cannot be folded into the policy.
	if err := ValidateDestination(&dest); err != nil {
		return err
	}
	for _, e := range existing {
		samePort := (e.Port == nil && dest.Port == nil) ||
			(e.Port != nil && dest.Port != nil && *e.Port == *dest.Port)
		if e.CIDR == dest.CIDR && samePort && e.Protocol == dest.Protocol {
			return nil // already present
		}
	}
	existing = append(existing, dest)
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
	SoakDays int                       // soak window applied (days)
	DryRun   bool                      // true = preview only, nothing written
	Pinned   bool                      // operator pin active → no rows flipped
	PinValue string                    // trimmed pin file contents, for display
	Eligible []models.UserEgressPolicy // mature LEARNING rows (would flip / did flip)
	Flipped  []string                  // user IDs actually flipped (empty on dry-run/pin)
	Failed   map[string]string         // user ID → upsert error, for the rows that failed
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
