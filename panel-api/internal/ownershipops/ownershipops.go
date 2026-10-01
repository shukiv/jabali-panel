// Package ownershipops runs the domain ownership-proof actions of GH #1816 /
// ADR-0170 that come after the create-time decision:
//
//   - a TXT check of a pending domain or web alias (the tenant's Verify now
//     button and the background ticker);
//   - an administrator's approval or revoke, with the parent-rule cascade;
//   - the expiry of names nobody proved within domainops.OwnershipExpiry.
//
// Every state change goes through the conditional writes of
// repository.DomainOwnershipRepository, and every side effect (a reconcile, a
// notification, a cascade) runs only for the caller whose write changed the
// row. A check that raced an approval, a revoke or another check does nothing.
package ownershipops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/audit"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Notification event kinds this package publishes.
const (
	EventVerified   = "domain.ownership.verified"
	EventRevoked    = "domain.ownership.revoked"
	EventExpiring   = "domain.ownership.expiring"
	EventExpired    = "domain.ownership.expired"
	EventNeedsAdmin = "domain.ownership.needs_admin"
)

// Errors the actions return.
var (
	// ErrPanelPrimary refuses a revoke of the panel's own row: its pending
	// shape would unpublish the panel's zone and stop its mail.
	ErrPanelPrimary = errors.New("ownershipops: the panel's own domain cannot be revoked")
	// ErrDockerAppDomain refuses a revoke of a docker-app domain: its pending
	// shape takes the app's vhost down. Remove the app instead.
	ErrDockerAppDomain = errors.New("ownershipops: a docker-app domain cannot be revoked")
)

// DomainReader reads domain rows.
type DomainReader interface {
	FindByID(ctx context.Context, id string) (*models.Domain, error)
	FindByName(ctx context.Context, name string) (*models.Domain, error)
}

// AliasReader reads web alias rows.
type AliasReader interface {
	FindByID(ctx context.Context, id string) (*models.WebDomainAlias, error)
}

// SettingsReader reads the server settings (this server's nameserver names
// and its DR role).
type SettingsReader interface {
	Get(ctx context.Context) (*models.ServerSettings, error)
}

// Notifier publishes a notification.
type Notifier interface {
	Publish(ctx context.Context, env notifications.Envelope) (string, error)
}

// Deps wires a Service.
type Deps struct {
	// Store and Domains are required.
	Store   repository.DomainOwnershipRepository
	Domains DomainReader
	// Aliases is required for the alias actions.
	Aliases AliasReader
	// Settings gives this server's NS1/NS2 names to a check and the DR role
	// to the ticker. Nil: no nameserver hint, and the ticker never runs.
	Settings SettingsReader
	// Lookups are the public-DNS reads. The zero value uses the public
	// resolvers.
	Lookups domainops.OwnershipLookups
	// Schedule converges one domain at once. Nil leaves it to the next
	// reconcile pass.
	Schedule func(domainID string)
	// Notify publishes the tenant and admin notifications. Nil publishes
	// nothing.
	Notify Notifier
	// Audit records the changes the service makes on its own (a DNS proof,
	// the parent rule, an expiry) as system events. An administrator's
	// approve or revoke is recorded by its caller, which knows the admin.
	// Nil records nothing.
	Audit audit.Recorder
	// Teardowns, Ports and Agent are handed to domainops.Delete when a name
	// expires. Agent also clears the mail login cache after a revoke. Nil
	// Agent skips that flush.
	Teardowns repository.DomainTeardownRepository
	Ports     repository.PortAllocationRepository
	Agent     agent.AgentInterface
	Now       func() time.Time
	Log       *slog.Logger
}

// Service runs the actions.
type Service struct{ d Deps }

// New returns a Service, or nil when a required dependency is missing.
func New(d Deps) *Service {
	if d.Store == nil || d.Domains == nil {
		return nil
	}
	if d.Lookups.TXT == nil {
		d.Lookups = domainops.PublicOwnershipLookups()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d}
}

func (s *Service) schedule(domainID string) {
	if s.d.Schedule != nil && domainID != "" {
		s.d.Schedule(domainID)
	}
}

// Audit actions of the changes the service makes on its own.
const (
	AuditVerify      = "domain.ownership.verify"
	AuditExpire      = "domain.ownership.expire"
	AuditAliasVerify = "domain.alias.ownership.verify"
	AuditAliasExpire = "domain.alias.ownership.expire"
)

func (s *Service) audit(subjectUserID, action, targetType, targetID string, meta map[string]any) {
	if s.d.Audit != nil {
		s.d.Audit.Record(audit.DomainOwnershipSystem(subjectUserID, action, targetType, targetID, meta))
	}
}

func (s *Service) publish(ctx context.Context, env notifications.Envelope) {
	if s.d.Notify == nil {
		return
	}
	if _, err := s.d.Notify.Publish(ctx, env); err != nil {
		s.d.Log.Warn("ownership: notification publish failed", "event", env.EventKind, "err", err)
	}
}

// ownNameservers returns this server's NS1/NS2 names, or nil.
func (s *Service) ownNameservers(ctx context.Context) []string {
	if s.d.Settings == nil {
		return nil
	}
	st, err := s.d.Settings.Get(ctx)
	if err != nil || st == nil {
		return nil
	}
	var out []string
	for _, n := range []string{st.NS1Name, st.NS2Name} {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// nextCheck returns when a row that did not verify is checked next. A name
// whose nameservers already point here cannot pass a DNS check until they
// change, so it is re-checked only daily (an administrator is told to
// approve it).
func nextCheck(result string, pendingSince *time.Time, now time.Time) time.Time {
	if result == models.OwnershipResultNSPointsHere {
		return now.Add(24 * time.Hour)
	}
	return domainops.NextOwnershipCheck(pendingSince, now)
}

// needsAdmin reports whether a check result means only an administrator can
// move the row forward.
func needsAdmin(result string) bool {
	return result == models.OwnershipResultNSPointsHere || result == models.OwnershipResultDNSUnresolvable
}

// labelDepth orders names shallow first, so a cascade settles a parent before
// it re-checks the names under it.
func labelDepth(name string) int { return strings.Count(strings.Trim(name, "."), ".") }

func under(name, parent string) bool {
	return strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(parent))
}

func tenantDomainLink(id string) string { return "/jabali-panel/domains/" + id }

const adminOwnershipLink = "/jabali-admin/domains/ownership"

// CheckDomain reads the challenge record of a pending domain and records the
// result. It returns the result; a verified domain returns
// models.OwnershipResultVerified without a lookup.
func (s *Service) CheckDomain(ctx context.Context, d *models.Domain) (string, error) {
	if d == nil {
		return "", repository.ErrNotFound
	}
	if d.Verified() {
		return models.OwnershipResultVerified, nil
	}
	token, err := s.domainToken(ctx, d)
	if err != nil {
		return "", err
	}
	result := domainops.CheckOwnership(ctx, s.d.Lookups, d.Name, token, s.ownNameservers(ctx))
	now := s.d.Now()
	if result == models.OwnershipResultVerified {
		changed, err := s.d.Store.MarkDomainVerified(ctx, d.ID, models.OwnershipMethodDNSTXT, token, now)
		if err != nil {
			return "", err
		}
		if changed {
			s.audit(d.UserID, AuditVerify, "domain", d.ID, map[string]any{"domain": d.Name, "method": models.OwnershipMethodDNSTXT})
			s.domainVerified(ctx, d, now)
		}
		return result, nil
	}
	changed, err := s.d.Store.RecordDomainCheck(ctx, d.ID, token, result, now, nextCheck(result, d.OwnershipPendingSince, now))
	if err != nil {
		return "", err
	}
	if changed && needsAdmin(result) && result != d.OwnershipLastResult {
		s.publish(ctx, notifications.Envelope{
			EventKind: EventNeedsAdmin,
			Severity:  models.NotificationSeverityWarning,
			Title:     "Domain needs approval: " + d.Name,
			Body:      needsAdminBody(d.Name, result),
			Deeplink:  adminOwnershipLink,
		})
	}
	return result, nil
}

func needsAdminBody(name, result string) string {
	if result == models.OwnershipResultNSPointsHere {
		return fmt.Sprintf("The nameservers of %s already point to this server, so its owner cannot prove it with a DNS record. Approve it if you know the owner.", name)
	}
	return fmt.Sprintf("The public DNS of %s does not answer, so its owner cannot prove it with a DNS record. Approve it if you know the owner.", name)
}

// domainToken returns the row's challenge token, storing a fresh one first
// when the row has none (a row an older binary inserted).
func (s *Service) domainToken(ctx context.Context, d *models.Domain) (string, error) {
	if d.OwnershipToken != "" {
		return d.OwnershipToken, nil
	}
	tok, err := domainops.NewOwnershipToken()
	if err != nil {
		return "", err
	}
	stored, err := s.d.Store.EnsureDomainToken(ctx, d.ID, tok)
	if err != nil {
		return "", err
	}
	if stored {
		d.OwnershipToken = tok
		return tok, nil
	}
	// Another writer set a token first: use the stored one.
	fresh, err := s.d.Domains.FindByID(ctx, d.ID)
	if err != nil {
		return "", err
	}
	d.OwnershipToken = fresh.OwnershipToken
	return fresh.OwnershipToken, nil
}

// domainVerified runs after the caller's write moved d to verified: the
// domain converges to its live shape, the owner and the admin bell hear of
// it, and the pending names under it that the parent rule now covers follow.
func (s *Service) domainVerified(ctx context.Context, d *models.Domain, now time.Time) {
	s.schedule(d.ID)
	s.notifyVerified(ctx, d)
	s.cascadeVerify(ctx, d.Name, now)
}

func (s *Service) notifyVerified(ctx context.Context, d *models.Domain) {
	body := fmt.Sprintf("%s is verified. Its DNS zone, certificate and mail are being set up now.", d.Name)
	s.publish(ctx, notifications.Envelope{
		EventKind: EventVerified, Severity: models.NotificationSeverityInfo,
		Title: "Domain verified: " + d.Name, Body: body,
		Deeplink: tenantDomainLink(d.ID), UserID: d.UserID,
	})
	s.publish(ctx, notifications.Envelope{
		EventKind: EventVerified, Severity: models.NotificationSeverityInfo,
		Title: "Domain verified: " + d.Name, Body: body,
		Deeplink: "/jabali-admin/domains",
	})
}

// cascadeVerify re-applies the parent rule to the pending domains and web
// aliases under name, shallowest first, after name was verified. A name the
// rule now covers is verified with method parent, exactly as if it had been
// created after name was proven.
func (s *Service) cascadeVerify(ctx context.Context, name string, now time.Time) {
	domains, err := s.d.Store.ListPendingDomains(ctx)
	if err != nil {
		s.d.Log.Warn("ownership: list pending domains for the parent rule failed", "parent", name, "err", err)
	} else {
		var below []models.Domain
		for _, c := range domains {
			if under(c.Name, name) {
				below = append(below, c)
			}
		}
		sort.SliceStable(below, func(i, j int) bool { return labelDepth(below[i].Name) < labelDepth(below[j].Name) })
		for i := range below {
			c := &below[i]
			if !domainops.ParentCovers(ctx, s.d.Domains, c.Name, c.UserID) {
				continue
			}
			changed, err := s.d.Store.MarkDomainVerified(ctx, c.ID, models.OwnershipMethodParent, c.OwnershipToken, now)
			if err != nil {
				s.d.Log.Warn("ownership: parent-rule verify failed", "domain", c.Name, "err", err)
				continue
			}
			if changed {
				s.audit(c.UserID, AuditVerify, "domain", c.ID, map[string]any{"domain": c.Name, "method": models.OwnershipMethodParent, "parent": name})
				s.schedule(c.ID)
				s.notifyVerified(ctx, c)
			}
		}
	}

	if s.d.Aliases == nil {
		return
	}
	aliases, err := s.d.Store.ListPendingAliases(ctx)
	if err != nil {
		s.d.Log.Warn("ownership: list pending aliases for the parent rule failed", "parent", name, "err", err)
		return
	}
	for i := range aliases {
		a := &aliases[i]
		if !under(a.Hostname, name) {
			continue
		}
		owner, err := s.d.Domains.FindByID(ctx, a.DomainID)
		if err != nil {
			continue
		}
		if !domainops.ParentCovers(ctx, s.d.Domains, a.Hostname, owner.UserID) {
			continue
		}
		changed, err := s.d.Store.MarkAliasVerified(ctx, a.ID, models.OwnershipMethodParent, a.OwnershipToken, now)
		if err != nil {
			s.d.Log.Warn("ownership: parent-rule alias verify failed", "alias", a.Hostname, "err", err)
			continue
		}
		if changed {
			s.audit(owner.UserID, AuditAliasVerify, "domain_alias", a.ID,
				map[string]any{"alias": a.Hostname, "domain": owner.Name, "method": models.OwnershipMethodParent, "parent": name})
			s.schedule(a.DomainID)
		}
	}
}

// ApproveDomain is an administrator's approval of a pending domain. It
// reports whether this call changed the row (false: it was not pending).
func (s *Service) ApproveDomain(ctx context.Context, id string) (bool, error) {
	d, err := s.d.Domains.FindByID(ctx, id)
	if err != nil {
		return false, err
	}
	now := s.d.Now()
	changed, err := s.d.Store.MarkDomainVerified(ctx, d.ID, models.OwnershipMethodAdmin, "", now)
	if err != nil || !changed {
		return false, err
	}
	s.domainVerified(ctx, d, now)
	return true, nil
}

// RevokeDomain is an administrator's revoke of a verified domain: the row
// goes back to pending with a fresh token, and every parent-proven domain
// and alias under it that the parent rule no longer covers follows. It
// reports whether this call changed the row itself.
//
// The cascade runs even when the row was already pending, so repeating a
// revoke finishes a cascade an earlier call could not complete. A revoked
// row keeps its verified_at, so it never expires: an administrator resolves
// it.
//
// When any domain went back to pending, its mailboxes must stop signing in.
// queryLogin refuses them on the next IMAP or SMTP login, but Stalwart
// answers JMAP (webmail) from its HTTP login cache, so the revoke also
// flushes that cache.
func (s *Service) RevokeDomain(ctx context.Context, id string) (bool, error) {
	d, err := s.d.Domains.FindByID(ctx, id)
	if err != nil {
		return false, err
	}
	if d.IsPanelPrimary {
		return false, ErrPanelPrimary
	}
	if d.ManagedBy == models.DomainManagedByDockerApp {
		return false, ErrDockerAppDomain
	}
	now := s.d.Now()
	changed, err := s.revokeDomainRow(ctx, d, now)
	if err != nil {
		return false, err
	}
	cascaded, err := s.cascadeRevoke(ctx, d.Name, now)
	if changed || cascaded {
		s.flushMailLogins(ctx, d.ID)
	}
	return changed, err
}

// flushMailLogins asks the agent to clear Stalwart's HTTP login cache.
// Best-effort: the rows are already pending, and an agent that predates the
// verb only logs the failure here.
func (s *Service) flushMailLogins(ctx context.Context, domainID string) {
	if s.d.Agent == nil {
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := s.d.Agent.Call(actx, "mail.auth_cache.flush", map[string]any{}); err != nil {
		s.d.Log.Warn("ownership: clearing the mail login cache after a revoke", "domain_id", domainID, "err", err)
	}
}

func (s *Service) revokeDomainRow(ctx context.Context, d *models.Domain, now time.Time) (bool, error) {
	tok, err := domainops.NewOwnershipToken()
	if err != nil {
		return false, err
	}
	changed, err := s.d.Store.MarkDomainPending(ctx, d.ID, tok, now, true, false)
	if err != nil || !changed {
		return false, err
	}
	s.schedule(d.ID)
	s.publish(ctx, notifications.Envelope{
		EventKind: EventRevoked, Severity: models.NotificationSeverityWarning,
		Title:    "Domain needs verification: " + d.Name,
		Body:     fmt.Sprintf("An administrator withdrew the verification of %s. It is offline until you prove you control it again.", d.Name),
		Deeplink: tenantDomainLink(d.ID), UserID: d.UserID,
	})
	return true, nil
}

// cascadeRevoke sends back to pending, shallowest first, every parent-proven
// domain and alias under name that the parent rule no longer covers. A name
// with its own proof in between keeps the names under it covered. It reports
// whether any domain row changed.
func (s *Service) cascadeRevoke(ctx context.Context, name string, now time.Time) (bool, error) {
	var firstErr error
	domainChanged := false
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	children, err := s.d.Store.ListParentProvenUnder(ctx, name)
	keep(err)
	sort.SliceStable(children, func(i, j int) bool { return labelDepth(children[i].Name) < labelDepth(children[j].Name) })
	for i := range children {
		c := &children[i]
		if domainops.ParentCovers(ctx, s.d.Domains, c.Name, c.UserID) {
			continue
		}
		changed, err := s.revokeDomainRow(ctx, c, now)
		keep(err)
		domainChanged = domainChanged || changed
	}

	aliases, err := s.d.Store.ListParentProvenAliasesUnder(ctx, name)
	keep(err)
	for i := range aliases {
		a := &aliases[i]
		owner, err := s.d.Domains.FindByID(ctx, a.DomainID)
		if err != nil {
			keep(err)
			continue
		}
		if domainops.ParentCovers(ctx, s.d.Domains, a.Hostname, owner.UserID) {
			continue
		}
		tok, err := domainops.NewOwnershipToken()
		if err != nil {
			keep(err)
			continue
		}
		changed, err := s.d.Store.MarkAliasPending(ctx, a.ID, tok, now, true, false)
		keep(err)
		if changed {
			s.schedule(a.DomainID)
		}
	}
	return domainChanged, firstErr
}

// CheckAlias is CheckDomain for a web alias.
func (s *Service) CheckAlias(ctx context.Context, a *models.WebDomainAlias) (string, error) {
	if a == nil {
		return "", repository.ErrNotFound
	}
	if a.Verified() {
		return models.OwnershipResultVerified, nil
	}
	token := a.OwnershipToken
	if token == "" {
		tok, err := domainops.NewOwnershipToken()
		if err != nil {
			return "", err
		}
		stored, err := s.d.Store.EnsureAliasToken(ctx, a.ID, tok)
		if err != nil {
			return "", err
		}
		if !stored {
			// Another writer set a token first; the next check reads it.
			return "", repository.ErrOwnershipChanged
		}
		token = tok
		a.OwnershipToken = tok
	}
	result := domainops.CheckOwnership(ctx, s.d.Lookups, a.Hostname, token, s.ownNameservers(ctx))
	now := s.d.Now()
	if result == models.OwnershipResultVerified {
		changed, err := s.d.Store.MarkAliasVerified(ctx, a.ID, models.OwnershipMethodDNSTXT, token, now)
		if err != nil {
			return "", err
		}
		if changed {
			meta := map[string]any{"alias": a.Hostname, "method": models.OwnershipMethodDNSTXT}
			owner := ""
			if d, err := s.d.Domains.FindByID(ctx, a.DomainID); err == nil {
				owner, meta["domain"] = d.UserID, d.Name
			}
			s.audit(owner, AuditAliasVerify, "domain_alias", a.ID, meta)
			s.schedule(a.DomainID)
		}
		return result, nil
	}
	if _, err := s.d.Store.RecordAliasCheck(ctx, a.ID, token, result, now, nextCheck(result, a.OwnershipPendingSince, now)); err != nil {
		return "", err
	}
	return result, nil
}

// ApproveAlias is an administrator's approval of a pending web alias.
func (s *Service) ApproveAlias(ctx context.Context, id string) (bool, error) {
	a, err := s.alias(ctx, id)
	if err != nil {
		return false, err
	}
	changed, err := s.d.Store.MarkAliasVerified(ctx, a.ID, models.OwnershipMethodAdmin, "", s.d.Now())
	if err != nil || !changed {
		return false, err
	}
	s.schedule(a.DomainID)
	return true, nil
}

// RevokeAlias is an administrator's revoke of a verified web alias.
func (s *Service) RevokeAlias(ctx context.Context, id string) (bool, error) {
	a, err := s.alias(ctx, id)
	if err != nil {
		return false, err
	}
	tok, err := domainops.NewOwnershipToken()
	if err != nil {
		return false, err
	}
	changed, err := s.d.Store.MarkAliasPending(ctx, a.ID, tok, s.d.Now(), true, false)
	if err != nil || !changed {
		return false, err
	}
	s.schedule(a.DomainID)
	return true, nil
}

func (s *Service) alias(ctx context.Context, id string) (*models.WebDomainAlias, error) {
	if s.d.Aliases == nil {
		return nil, repository.ErrNotFound
	}
	return s.d.Aliases.FindByID(ctx, id)
}
