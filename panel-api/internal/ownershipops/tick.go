package ownershipops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	// TickInterval is how often the ticker runs. It must not exceed a
	// minute, the first step of the re-check backoff.
	TickInterval = time.Minute
	// tickBatch caps the checks of one kind per tick; the rest are due on
	// the next one.
	tickBatch = 50
	// checkTimeout bounds one check (three public resolvers plus an NS read).
	checkTimeout = 30 * time.Second
	// expireTimeout bounds one expiry delete, host teardown included. A
	// teardown that does not finish leaves its tombstone for the reconciler.
	expireTimeout = 3 * time.Minute
)

// Start runs Tick every TickInterval until ctx ends. The first pass waits
// 30 seconds so a starting panel settles first.
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	first := time.NewTimer(30 * time.Second)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	s.Tick(ctx)
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// active reports whether this server may run the ticker: it is a primary.
// A standby's rows come from its primary, so it neither checks nor deletes;
// an unreadable role skips the tick too (a skipped tick only delays).
func (s *Service) active(ctx context.Context) bool {
	if s.d.Settings == nil {
		return false
	}
	st, err := s.d.Settings.Get(ctx)
	if err != nil || st == nil {
		return false
	}
	return st.IsPrimary()
}

// Tick runs the due checks and the expiry sweep once.
func (s *Service) Tick(ctx context.Context) {
	if !s.active(ctx) {
		return
	}
	now := s.d.Now()
	if due, err := s.d.Store.ListDomainsDue(ctx, now, tickBatch); err != nil {
		s.d.Log.Warn("ownership: list due domains failed", "err", err)
	} else {
		for i := range due {
			cctx, cancel := context.WithTimeout(ctx, checkTimeout)
			if _, err := s.CheckDomain(cctx, &due[i]); err != nil {
				s.d.Log.Warn("ownership: domain check failed", "domain", due[i].Name, "err", err)
			}
			cancel()
		}
	}
	if s.d.Aliases != nil {
		if due, err := s.d.Store.ListAliasesDue(ctx, now, tickBatch); err != nil {
			s.d.Log.Warn("ownership: list due aliases failed", "err", err)
		} else {
			for i := range due {
				cctx, cancel := context.WithTimeout(ctx, checkTimeout)
				if _, err := s.CheckAlias(cctx, &due[i]); err != nil {
					s.d.Log.Warn("ownership: alias check failed", "alias", due[i].Hostname, "err", err)
				}
				cancel()
			}
		}
	}
	s.sweepExpiry(ctx)
}

// sweepExpiry sends the expiry notice and releases the names nobody proved.
func (s *Service) sweepExpiry(ctx context.Context) {
	now := s.d.Now()
	domains, err := s.d.Store.ListPendingDomains(ctx)
	if err != nil {
		s.d.Log.Warn("ownership: list pending domains for expiry failed", "err", err)
	}
	for i := range domains {
		d := &domains[i]
		exp := domainops.DomainOwnershipExpires(d)
		if exp == nil {
			continue
		}
		if !now.Before(*exp) {
			s.expireDomain(ctx, d, now)
			continue
		}
		if d.OwnershipExpiryNotifiedAt == nil && !now.Before(exp.Add(-domainops.OwnershipExpiryNotice)) {
			s.noticeDomainExpiry(ctx, d, *exp, now)
		}
	}

	if s.d.Aliases == nil {
		return
	}
	aliases, err := s.d.Store.ListPendingAliases(ctx)
	if err != nil {
		s.d.Log.Warn("ownership: list pending aliases for expiry failed", "err", err)
		return
	}
	for i := range aliases {
		a := &aliases[i]
		exp := domainops.OwnershipExpiresAt(a.OwnershipState)
		if exp == nil {
			continue
		}
		if !now.Before(*exp) {
			s.expireAlias(ctx, a, now)
			continue
		}
		if a.OwnershipExpiryNotifiedAt == nil && !now.Before(exp.Add(-domainops.OwnershipExpiryNotice)) {
			s.noticeAliasExpiry(ctx, a, *exp, now)
		}
	}
}

func (s *Service) noticeDomainExpiry(ctx context.Context, d *models.Domain, exp, now time.Time) {
	if err := s.d.Store.MarkDomainExpiryNotified(ctx, d.ID, now); err != nil {
		s.d.Log.Warn("ownership: record expiry notice failed", "domain", d.Name, "err", err)
		return
	}
	s.publish(ctx, notifications.Envelope{
		EventKind: EventExpiring, Severity: models.NotificationSeverityWarning,
		Title: "Domain not verified yet: " + d.Name,
		Body: fmt.Sprintf("%s is still not verified. Unless you prove you control it, it is removed from your account on %s. Your site files stay.",
			d.Name, exp.UTC().Format("2 January 2006")),
		Deeplink: tenantDomainLink(d.ID), UserID: d.UserID,
	})
}

func (s *Service) noticeAliasExpiry(ctx context.Context, a *models.WebDomainAlias, exp, now time.Time) {
	if err := s.d.Store.MarkAliasExpiryNotified(ctx, a.ID, now); err != nil {
		s.d.Log.Warn("ownership: record alias expiry notice failed", "alias", a.Hostname, "err", err)
		return
	}
	owner, err := s.d.Domains.FindByID(ctx, a.DomainID)
	if err != nil {
		return
	}
	s.publish(ctx, notifications.Envelope{
		EventKind: EventExpiring, Severity: models.NotificationSeverityWarning,
		Title: "Alias not verified yet: " + a.Hostname,
		Body: fmt.Sprintf("The alias %s of %s is still not verified. Unless you prove you control it, it is removed on %s.",
			a.Hostname, owner.Name, exp.UTC().Format("2 January 2006")),
		Deeplink: tenantDomainLink(owner.ID), UserID: owner.UserID,
	})
}

// expiredRows is the row deleter the expiry hands to domainops.Delete: the
// row goes only while it is still an expired claim. When a verification won
// the race, the delete fails, and domainops.Delete drops its tombstone and
// runs nothing host-side.
type expiredRows struct {
	store  repository.DomainOwnershipRepository
	cutoff time.Time
}

func (e expiredRows) Delete(ctx context.Context, id string) error {
	return e.store.DeleteExpiredDomain(ctx, id, e.cutoff)
}

func (s *Service) expireDomain(ctx context.Context, d *models.Domain, now time.Time) {
	dctx, cancel := context.WithTimeout(ctx, expireTimeout)
	defer cancel()
	_, err := domainops.Delete(dctx, domainops.DeleteDeps{
		Domains:   expiredRows{store: s.d.Store, cutoff: now.Add(-domainops.OwnershipExpiry)},
		Teardowns: s.d.Teardowns,
		Ports:     s.d.Ports,
		Agent:     s.d.Agent,
		Log:       s.d.Log,
	}, d.ID, d.Name, false)
	if errors.Is(err, repository.ErrOwnershipChanged) {
		return // verified or changed meanwhile: it stays
	}
	if err != nil {
		s.d.Log.Warn("ownership: expiry delete failed", "domain", d.Name, "err", err)
		return
	}
	s.d.Log.Info("ownership: unverified domain expired and was removed", "domain", d.Name, "domain_id", d.ID, "user_id", d.UserID)
	s.audit(d.UserID, AuditExpire, "domain", d.ID, map[string]any{"domain": d.Name, "pending_since": d.OwnershipPendingSince})
	body := fmt.Sprintf("%s was never verified, so it was removed after %d days. Its site files were kept. You can add it again once you can prove you control it.",
		d.Name, int(domainops.OwnershipExpiry/(24*time.Hour)))
	s.publish(ctx, notifications.Envelope{
		EventKind: EventExpired, Severity: models.NotificationSeverityWarning,
		Title: "Domain removed: " + d.Name, Body: body,
		Deeplink: "/jabali-panel/domains", UserID: d.UserID,
	})
	s.publish(ctx, notifications.Envelope{
		EventKind: EventExpired, Severity: models.NotificationSeverityInfo,
		Title: "Unverified domain removed: " + d.Name, Body: body,
		Deeplink: "/jabali-admin/domains",
	})
}

func (s *Service) expireAlias(ctx context.Context, a *models.WebDomainAlias, now time.Time) {
	err := s.d.Store.DeleteExpiredAlias(ctx, a.ID, now.Add(-domainops.OwnershipExpiry))
	if errors.Is(err, repository.ErrOwnershipChanged) {
		return
	}
	if err != nil {
		s.d.Log.Warn("ownership: alias expiry delete failed", "alias", a.Hostname, "err", err)
		return
	}
	s.d.Log.Info("ownership: unverified alias expired and was removed", "alias", a.Hostname, "domain_id", a.DomainID)
	s.schedule(a.DomainID)
	owner, err := s.d.Domains.FindByID(ctx, a.DomainID)
	if err != nil {
		s.audit("", AuditAliasExpire, "domain_alias", a.ID, map[string]any{"alias": a.Hostname, "pending_since": a.OwnershipPendingSince})
		return
	}
	s.audit(owner.UserID, AuditAliasExpire, "domain_alias", a.ID,
		map[string]any{"alias": a.Hostname, "domain": owner.Name, "pending_since": a.OwnershipPendingSince})
	s.publish(ctx, notifications.Envelope{
		EventKind: EventExpired, Severity: models.NotificationSeverityWarning,
		Title: "Alias removed: " + a.Hostname,
		Body: fmt.Sprintf("The alias %s of %s was never verified, so it was removed after %d days.",
			a.Hostname, owner.Name, int(domainops.OwnershipExpiry/(24*time.Hour))),
		Deeplink: tenantDomainLink(owner.ID), UserID: owner.UserID,
	})
}
