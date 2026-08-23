// Package repository wraps the panel's MariaDB access behind small, testable
// interfaces. Each repository has one responsibility (users, refresh tokens,
// etc.). Service layers depend on these interfaces, not on *gorm.DB directly,
// so tests can swap in mocks.
package repository

import "errors"

// ErrNotFound is returned when a lookup by a unique key (email, id, token
// hash) finds no row. Wraps into gorm.ErrRecordNotFound for callers that
// check repository errors, without leaking the GORM type.
var ErrNotFound = errors.New("repository: not found")

// ErrConflict is returned when a write violates a unique constraint
// (duplicate email, duplicate token_hash). Callers translate this into
// HTTP 409 at the handler boundary.
var ErrConflict = errors.New("repository: conflict")

// ErrLocked is returned when a SELECT ... FOR UPDATE NOWAIT fails to acquire
// the lock immediately (another transaction holds it). This indicates a concurrent
// attempt to consume the same magic link token.
var ErrLocked = errors.New("repository: locked")

// ErrAlreadyUsed is returned when a token has already been consumed (UsedAt is not nil).
// This prevents single-use enforcement from being bypassed.
var ErrAlreadyUsed = errors.New("repository: already used")

// ErrFtpCapExceeded is returned by ReserveWithinCap when the tenant already
// holds max_ftp_accounts subaccounts. Enforced under a per-tenant lock so
// concurrent creates get a deterministic 409 instead of racing past the cap
// (JAB-262).
var ErrFtpCapExceeded = errors.New("repository: ftp account cap exceeded")

// ErrFtpQuotaSplitExceeded is returned by ReserveWithinCap when adding an
// isolated subaccount's quota_mb would push the sum of the tenant's isolated
// quotas over the package disk quota (JAB-262).
var ErrFtpQuotaSplitExceeded = errors.New("repository: ftp isolated quota split exceeded")

// ErrLogStreamCapExceeded is returned by LogAccessStreamRepository.ReserveWithinCap
// when the user already holds the maximum active log-stream grants. The
// reservation locks the tenant's users row FOR UPDATE, so concurrent creates get
// a deterministic error instead of racing past the cap (JAB-347).
var ErrLogStreamCapExceeded = errors.New("repository: log stream cap exceeded")

// ErrPanelPrimaryNotFound is returned by DomainRepository.FindPanelPrimary
// when no row has is_panel_primary=1. Distinct from ErrNotFound so the
// Settings → Email endpoint can differentiate "row missing, return 202
// initializing" from "unrelated lookup failed". See ADR-0048.
var ErrPanelPrimaryNotFound = errors.New("repository: panel primary domain not found")

// ErrCannotDeletePanelPrimary is returned by DomainRepository.Delete when
// the caller tries to delete a row with is_panel_primary=1. Translated
// to HTTP 403 with code "panel_primary_protected" at the handler layer.
// See ADR-0048.
var ErrCannotDeletePanelPrimary = errors.New("repository: cannot delete panel primary domain")
