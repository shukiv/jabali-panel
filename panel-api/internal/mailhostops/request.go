package mailhostops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Refusal kinds. Request wraps every refusal in one of them, so an adapter
// maps a kind to a status and shows the wrapped reason as is. Any other
// error is a lookup or store failure.
var (
	ErrInvalidName = errors.New("mailhostops: invalid mail hostname")
	ErrNotReady    = errors.New("mailhostops: the panel is not ready to change the mail hostname")
	ErrNameRefused = errors.New("mailhostops: the mail hostname is refused")
)

type kindError struct{ kind, cause error }

func (e *kindError) Error() string   { return e.cause.Error() }
func (e *kindError) Unwrap() []error { return []error{e.kind, e.cause} }

// SettingsReader reads the server settings row.
type SettingsReader interface {
	Get(ctx context.Context) (*models.ServerSettings, error)
}

// PanelCertReader reads a panel certificate row by kind.
type PanelCertReader interface {
	GetByKind(ctx context.Context, kind string) (*models.PanelCertificate, error)
}

// RequestDomains looks up hosted domains and the panel-primary one.
type RequestDomains interface {
	DomainFinder
	FindPanelPrimary(ctx context.Context) (*models.Domain, error)
}

// SwitchoverStore records and withdraws the switchover request.
type SwitchoverStore interface {
	Request(ctx context.Context, desired, requestedBy string, now time.Time) error
	Cancel(ctx context.Context, now time.Time) error
}

// RequestDeps are Request's and Cancel's collaborators. Aliases may be nil
// when web aliases are not wired; Now defaults to time.Now.
type RequestDeps struct {
	Settings   SettingsReader
	PanelCerts PanelCertReader
	Domains    RequestDomains
	Aliases    AliasFinder
	Switchover SwitchoverStore
	Now        func() time.Time
}

func (d RequestDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Request asks the reconciler to move the panel mail hostname to desired
// and returns the normalized name. desired must be a valid bare hostname,
// the panel must be ready (Ready), desired must not already be the effective
// mail hostname, and CheckName must clear it. Switching back to the derived
// mail.<hostname> is a request like any other. The request replaces an
// earlier one unless an attempt is issuing (repository.ErrSwitchoverInFlight).
func Request(ctx context.Context, d RequestDeps, desired, requestedBy string) (string, error) {
	norm, err := models.ValidateMailHostname(desired)
	if err != nil {
		return "", &kindError{kind: ErrInvalidName, cause: err}
	}
	s, err := d.Settings.Get(ctx)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		s = nil
	case err != nil:
		return "", fmt.Errorf("read server settings: %w", err)
	}
	hostRow, err := d.PanelCerts.GetByKind(ctx, models.PanelCertKindHostname)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return "", fmt.Errorf("read panel certificate: %w", err)
	}
	primary, err := d.Domains.FindPanelPrimary(ctx)
	if err != nil && !errors.Is(err, repository.ErrPanelPrimaryNotFound) {
		return "", fmt.Errorf("read panel-primary domain: %w", err)
	}
	if err := Ready(s, hostRow, primary); err != nil {
		return "", &kindError{kind: ErrNotReady, cause: err}
	}
	if norm == models.EffectiveMailHostname(s.MailHostname, s.Hostname) {
		return "", &kindError{kind: ErrNameRefused, cause: ErrNameAlreadyApplied}
	}
	if err := CheckName(ctx, NameDeps{Domains: d.Domains, Aliases: d.Aliases}, s, norm); err != nil {
		if errors.Is(err, ErrNameIsPanelHostname) || errors.Is(err, ErrNameClaimedByDomain) ||
			errors.Is(err, ErrNameIsAlias) || errors.Is(err, ErrNameHasAliasUnder) {
			return "", &kindError{kind: ErrNameRefused, cause: err}
		}
		return "", err
	}
	if err := d.Switchover.Request(ctx, norm, requestedBy, d.now()); err != nil {
		return "", err
	}
	return norm, nil
}

// Cancel withdraws a pending or failed request. It returns
// repository.ErrSwitchoverInFlight while an attempt is issuing and
// repository.ErrNotFound when there is nothing to cancel.
func Cancel(ctx context.Context, d RequestDeps) error {
	return d.Switchover.Cancel(ctx, d.now())
}
