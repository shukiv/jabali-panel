package domainops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/dnsverify"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Domain ownership proof (GH #1816 / ADR-0170).
//
// A name a tenant adds stays pending until its owner proves control of it:
// a TXT record _jabali-challenge.<name> = jabali-verify=<token>, read only
// through public resolvers, two of three agreeing. While pending, the
// reconciler keeps it off the public (no published zone, no recursor forward,
// no mail, no trusted certificate, a vhost only the preview URL reaches).
//
// This file holds the rules every door shares: who is verified at create time
// (DecideOwnership), what a TXT check found (CheckOwnership), when the next
// check is due, and when a name expires.

// OwnershipVerified is the one predicate every gate uses. A nil domain, an
// empty status and any unknown status all count as pending.
func OwnershipVerified(d *models.Domain) bool {
	return d != nil && d.OwnershipState.Verified()
}

// NewOwnershipToken returns a fresh challenge token: 32 random bytes,
// hex-encoded (64 characters). A token is never reused across rows.
func NewOwnershipToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// OwnershipChallengeName is the name the challenge TXT record sits at.
func OwnershipChallengeName(name string) string {
	return models.OwnershipChallengePrefix + NormalizeDomainName(name)
}

// OwnershipChallengeValue is the TXT value that proves token.
func OwnershipChallengeValue(token string) string {
	return models.OwnershipValuePrefix + token
}

// PreviewGate is the per-domain value the preview URL's proxy sends to reach a
// pending domain's own vhost. The vhost answers every other request for the
// real name with 444 (ADR-0170 section 2), so a name whose public A record
// still points here serves nothing to the public while it is unproven.
//
// It is not a secret the tenant must not know: a browser never adds this
// header on its own, so knowing it cannot make a visitor's request pass. It
// is derived, not stored, and changes whenever the token does.
func PreviewGate(d *models.Domain) string {
	if d == nil {
		return ""
	}
	sum := sha256.Sum256([]byte("jabali-preview-gate\x00" + d.ID + "\x00" + d.OwnershipToken))
	return hex.EncodeToString(sum[:16])
}

// OwnershipPolicyReader reads the proof-required switch.
type OwnershipPolicyReader interface {
	GetSettings(ctx context.Context) (models.DomainOwnershipSettings, error)
}

// ProofRequired reports whether new names need proof. A nil reader or a read
// error counts as required: the switch fails closed.
func ProofRequired(ctx context.Context, p OwnershipPolicyReader) bool {
	if p == nil {
		return true
	}
	s, err := p.GetSettings(ctx)
	if err != nil {
		return true
	}
	return s.RequireProof
}

// OwnershipAssertion is an adapter's explicit statement that a name is
// already proven. It is separate from the actor's privilege on purpose: the
// migration and restore doors run the name guards as a non-admin (GH #1898)
// while an admin still vouches for the names (ADR-0170 section 4).
type OwnershipAssertion struct {
	// Method is how the adapter knows: admin, migration, restore or
	// automation. Any other value is ignored and the name stays pending.
	Method string
}

// ErrOwnershipAssertion means an assertion named a method no adapter may
// claim. It is a programming error, never a policy result.
var ErrOwnershipAssertion = errors.New("domainops: invalid ownership assertion")

func assertableMethod(m string) bool {
	switch m {
	case models.OwnershipMethodAdmin, models.OwnershipMethodMigration,
		models.OwnershipMethodRestore, models.OwnershipMethodAutomation:
		return true
	}
	return false
}

// OwnershipDecision is a name's state at create time.
type OwnershipDecision struct {
	Verified bool
	// Method is how it is verified; "" when pending.
	Method string
}

// AncestorFinder looks a hosted domain up by name.
type AncestorFinder interface {
	FindByName(ctx context.Context, name string) (*models.Domain, error)
}

// OwnershipDeps are the stores DecideOwnership reads. A nil Policy means
// proof is required; a nil Domains skips the parent rule.
type OwnershipDeps struct {
	Domains AncestorFinder
	Policy  OwnershipPolicyReader
}

// DecideOwnership returns the state a new name starts in, first match wins:
//
//  1. the proof requirement is off: verified, method policy_off;
//  2. the adapter asserts it (admin-run migration or restore, a billing
//     token holding assert:domain_ownership): verified with that method;
//  3. the actor is an administrator: verified, method admin;
//  4. the parent rule: verified, method parent;
//  5. otherwise pending.
//
// The parent rule looks only at the NEAREST hosted ancestor. It covers the
// name when that ancestor is verified and is owned by ownerID or delegates
// its subdomains (GH #1812): consent from someone who proved the parent. A
// pending nearest ancestor keeps the name pending even when a verified domain
// sits further up. A lookup error keeps the name pending (fail closed).
func DecideOwnership(ctx context.Context, d OwnershipDeps, name, ownerID string, actorIsAdmin bool, assert *OwnershipAssertion) (OwnershipDecision, error) {
	if !ProofRequired(ctx, d.Policy) {
		return OwnershipDecision{Verified: true, Method: models.OwnershipMethodPolicyOff}, nil
	}
	if assert != nil {
		if !assertableMethod(assert.Method) {
			return OwnershipDecision{}, ErrOwnershipAssertion
		}
		return OwnershipDecision{Verified: true, Method: assert.Method}, nil
	}
	if actorIsAdmin {
		return OwnershipDecision{Verified: true, Method: models.OwnershipMethodAdmin}, nil
	}
	if ParentCovers(ctx, d.Domains, name, ownerID) {
		return OwnershipDecision{Verified: true, Method: models.OwnershipMethodParent}, nil
	}
	return OwnershipDecision{}, nil
}

// ParentCovers applies the parent rule (see DecideOwnership) to name for a
// row owned by ownerID. The ownership cascades re-run it against fresh rows:
// a revoke sends the parent-proven rows under the revoked name back to
// pending when it no longer covers them, and a verification lets pending
// rows under the verified name go live when it now does.
func ParentCovers(ctx context.Context, domains AncestorFinder, name, ownerID string) bool {
	if domains == nil {
		return false
	}
	for _, anc := range AncestorDomains(NormalizeDomainName(name)) {
		p, err := domains.FindByName(ctx, anc)
		if errors.Is(err, repository.ErrNotFound) || (err == nil && p == nil) {
			continue // not hosted here: keep walking up
		}
		if err != nil {
			return false
		}
		// The nearest hosted ancestor decides.
		return OwnershipVerified(p) && (p.UserID == ownerID || p.AllowSubdomainDelegation)
	}
	return false
}

// ApplyOwnershipDecision writes a decision onto a new row's state before the
// insert. A pending row gets a token, a pending-since time and an immediate
// first check.
func ApplyOwnershipDecision(st *models.OwnershipState, dec OwnershipDecision, now time.Time) error {
	token, err := NewOwnershipToken()
	if err != nil {
		return err
	}
	st.OwnershipToken = token
	if dec.Verified {
		st.OwnershipStatus = models.OwnershipVerified
		st.OwnershipMethod = dec.Method
		st.OwnershipVerifiedAt = &now
		st.OwnershipLastResult = models.OwnershipResultVerified
		return nil
	}
	st.OwnershipStatus = models.OwnershipPending
	st.OwnershipMethod = ""
	st.OwnershipPendingSince = &now
	st.OwnershipNextCheckAt = &now
	return nil
}

// StampOwnership makes the create-time ownership decision for a row a door
// builds itself (the panel-primary row, docker-app domains) and records it
// on d, exactly as Create does: decided against d.Name and d.UserID, with a
// fresh challenge token either way.
func StampOwnership(ctx context.Context, deps OwnershipDeps, d *models.Domain, actorIsAdmin bool, now time.Time) error {
	dec, err := DecideOwnership(ctx, deps, d.Name, d.UserID, actorIsAdmin, nil)
	if err != nil {
		return err
	}
	return ApplyOwnershipDecision(&d.OwnershipState, dec, now)
}

// ErrRenameUnprovenMail refuses a rename to an unproven name for a domain
// whose mail is registered with Stalwart (GH #1816 / ADR-0170 section 5):
// the rename carries the Stalwart domain, its DKIM key and its mailboxes to
// the new name, which nobody has proven.
var ErrRenameUnprovenMail = errors.New("domainops: the new name is not proven and this domain's mail would move to it")

// PlanRenameOwnership decides the ownership of a domain's new name exactly as
// a create would (the actor, the parent rule, the switch). The domain's own
// current name never counts as the new name's parent: it is about to be
// renamed away. A pending outcome for a domain with registered mail is
// refused with ErrRenameUnprovenMail.
func PlanRenameOwnership(ctx context.Context, deps OwnershipDeps, d *models.Domain, newName string, actorIsAdmin bool) (OwnershipDecision, error) {
	if deps.Domains != nil {
		deps.Domains = excludingFinder{inner: deps.Domains, name: d.Name}
	}
	dec, err := DecideOwnership(ctx, deps, newName, d.UserID, actorIsAdmin, nil)
	if err != nil {
		return OwnershipDecision{}, err
	}
	if !dec.Verified && d.DkimSelector != nil && *d.DkimSelector != "" {
		return OwnershipDecision{}, ErrRenameUnprovenMail
	}
	return dec, nil
}

// excludingFinder hides one name (the domain being renamed) from the parent
// rule's ancestor walk.
type excludingFinder struct {
	inner AncestorFinder
	name  string
}

func (f excludingFinder) FindByName(ctx context.Context, name string) (*models.Domain, error) {
	if strings.EqualFold(NormalizeDomainName(name), NormalizeDomainName(f.name)) {
		return nil, repository.ErrNotFound
	}
	return f.inner.FindByName(ctx, name)
}

// OwnershipLookups are the public-DNS reads a check makes. Tests replace them.
type OwnershipLookups struct {
	// TXT returns one answer per public resolver.
	TXT func(ctx context.Context, name string) []dnsverify.TXTAnswer
	// NS returns the name's public nameservers; queried=false means no
	// resolver could be reached.
	NS func(ctx context.Context, name string) (hosts []string, queried bool)
}

// PublicOwnershipLookups reads through the public resolvers only.
func PublicOwnershipLookups() OwnershipLookups {
	return OwnershipLookups{TXT: dnsverify.LookupTXTEachExternal, NS: dnsverify.LookupNSExternal}
}

// ownershipMatchesRequired is how many resolvers must return the value.
const ownershipMatchesRequired = 2

// CheckOwnership reads the challenge record for name and returns what it
// found: models.OwnershipResultVerified only when at least two public
// resolvers return exactly jabali-verify=<token>. ownNS are this server's own
// nameserver names (server settings NS1/NS2), used to recognise a name whose
// public delegation already points here, where no DNS proof can pass.
func CheckOwnership(ctx context.Context, look OwnershipLookups, name, token string, ownNS []string) string {
	if token == "" || look.TXT == nil {
		return models.OwnershipResultNotFound
	}
	want := OwnershipChallengeValue(token)
	answers := look.TXT(ctx, OwnershipChallengeName(name))
	matches, definitive, withRecords := 0, 0, 0
	for _, a := range answers {
		if !a.Definitive {
			continue
		}
		definitive++
		if len(a.Records) > 0 {
			withRecords++
		}
		for _, rec := range a.Records {
			if rec == want {
				matches++
				break
			}
		}
	}
	if matches >= ownershipMatchesRequired {
		return models.OwnershipResultVerified
	}
	if matches == 1 {
		return models.OwnershipResultPropagating
	}
	if look.NS != nil {
		if hosts, queried := look.NS(ctx, NormalizeDomainName(name)); queried && nsAllOwn(hosts, ownNS) {
			return models.OwnershipResultNSPointsHere
		}
	}
	if definitive == 0 {
		// No resolver answered for the challenge name. If a resolver still
		// answers for the name's top-level domain, public DNS is reachable and
		// the name's own DNS is broken (a lame delegation, often nameservers
		// that already point at this server with the zone unpublished).
		if look.NS != nil {
			if _, queried := look.NS(ctx, topLevelLabel(name)); queried {
				return models.OwnershipResultDNSUnresolvable
			}
		}
		return models.OwnershipResultResolversUnreachable
	}
	if withRecords > 0 {
		return models.OwnershipResultMismatch
	}
	return models.OwnershipResultNotFound
}

// nsAllOwn reports whether hosts is non-empty and every host is one of own.
func nsAllOwn(hosts, own []string) bool {
	if len(hosts) == 0 || len(own) == 0 {
		return false
	}
	ownSet := map[string]bool{}
	for _, o := range own {
		o = strings.TrimSuffix(NormalizeDomainName(o), ".")
		if o != "" {
			ownSet[o] = true
		}
	}
	if len(ownSet) == 0 {
		return false
	}
	for _, h := range hosts {
		if !ownSet[strings.TrimSuffix(NormalizeDomainName(h), ".")] {
			return false
		}
	}
	return true
}

func topLevelLabel(name string) string {
	name = strings.TrimSuffix(NormalizeDomainName(name), ".")
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// NextOwnershipCheck returns when a pending row is checked next. Checks are
// frequent at first, when an owner who just published the record is waiting,
// and back off from there: every minute for the first 5 minutes, every 5
// minutes to 30 minutes, every 15 minutes to an hour, hourly to 7 days, then
// daily until the name expires.
func NextOwnershipCheck(pendingSince *time.Time, now time.Time) time.Time {
	age := time.Duration(0)
	if pendingSince != nil {
		age = now.Sub(*pendingSince)
	}
	switch {
	case age < 5*time.Minute:
		return now.Add(time.Minute)
	case age < 30*time.Minute:
		return now.Add(5 * time.Minute)
	case age < time.Hour:
		return now.Add(15 * time.Minute)
	case age < 7*24*time.Hour:
		return now.Add(time.Hour)
	default:
		return now.Add(24 * time.Hour)
	}
}

// Pending names that were never verified expire after OwnershipExpiry, with a
// notice OwnershipExpiryNotice before (ADR-0170 open decision 4).
const (
	OwnershipExpiry       = 14 * 24 * time.Hour
	OwnershipExpiryNotice = 4 * 24 * time.Hour
)

// OwnershipExpiresAt returns when a pending state expires, or nil when it
// never does: a verified row, a row that was verified before (an admin
// revoke keeps it until an admin acts), or a row with no pending-since time.
func OwnershipExpiresAt(st models.OwnershipState) *time.Time {
	if st.Verified() || st.OwnershipVerifiedAt != nil || st.OwnershipPendingSince == nil {
		return nil
	}
	t := st.OwnershipPendingSince.Add(OwnershipExpiry)
	return &t
}

// DomainOwnershipExpires reports whether a pending domain expires at all.
// Besides OwnershipExpiresAt's rules, the panel's own row and a docker-app
// domain never expire: deleting either would strand what depends on it.
func DomainOwnershipExpires(d *models.Domain) *time.Time {
	if d == nil || d.IsPanelPrimary || d.ManagedBy == models.DomainManagedByDockerApp {
		return nil
	}
	return OwnershipExpiresAt(d.OwnershipState)
}
