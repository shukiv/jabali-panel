package domainops

import (
	"context"
	"encoding/json"
	"errors"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Reverse-proxy port reservation and row persistence (JAB-279 AC1/AC4/AC6). A
// reverse-proxy domain (GH #1175) draws a loopback port from the shared
// allocator before its row is inserted, and a tenant may name the exact port
// (GH #1401). The REST create door (createDomainOp) and the operator CLI
// (`jabali domain create --reverse-proxy`) each carried their own copy of the
// validate → system-uid probe → allocate → insert → release-on-failure
// sequence; the copies had already drifted (the CLI hard-coded the uid floor).
// Both now route through the functions below.
//
// Unlike the pure leaves in this package, these touch the port pool, the
// domains table, and the agent, so they take their dependencies explicitly.
// Owner resolution and authorization stay adapter-side (ADR-0083); every error
// is a typed sentinel the adapter maps to its own transport.

// ReverseProxyTenantUIDFloor mirrors the agent's minTenantUID (GH #1401): a
// loopback listener owned by a uid below this is a system or service process,
// not a tenant app, so a reverse-proxy target must not point at it.
const ReverseProxyTenantUIDFloor = 1000

// loopbackListenerUIDCommand is the agent verb that reports whether a loopback
// port is LISTENing and under which uid.
const loopbackListenerUIDCommand = "net.loopback_listener_uid"

var (
	// ErrReverseProxyUnavailable means this host has no port pool configured,
	// so reverse-proxy domains cannot be created at all.
	ErrReverseProxyUnavailable = errors.New("domainops: reverse-proxy domains are not enabled on this host")
	// ErrReverseProxyPortInvalid means the requested port failed the static
	// policy (privileged range, allocator pools, FTP passive range, or a fixed
	// jabali service port). The concrete reason is carried by *InvalidPortError.
	ErrReverseProxyPortInvalid = errors.New("domainops: reverse-proxy port is not allowed")
	// ErrReverseProxyPortSystemBound means a system or service uid already
	// LISTENs on the requested loopback port.
	ErrReverseProxyPortSystemBound = errors.New("domainops: reverse-proxy port is in use by a system service")
	// ErrReverseProxyPortInUse means the requested port is already reserved by
	// another domain.
	ErrReverseProxyPortInUse = errors.New("domainops: reverse-proxy port is already assigned to another domain")
	// ErrReverseProxyPortUnavailable means the allocator could not reserve a
	// port (pool exhausted, or a store error).
	ErrReverseProxyPortUnavailable = errors.New("domainops: no reverse-proxy port could be reserved")

	// ErrDomainExists means the insert hit the unique name constraint.
	ErrDomainExists = errors.New("domainops: domain already exists")
	// ErrPersist means the insert failed for any other reason.
	ErrPersist = errors.New("domainops: domain row not persisted")
)

// InvalidPortError carries the reason the static port policy gave, so an
// adapter can show it verbatim. errors.Is(err, ErrReverseProxyPortInvalid)
// matches it; Error() returns the reason alone.
type InvalidPortError struct {
	Reason error
}

func (e *InvalidPortError) Error() string   { return e.Reason.Error() }
func (e *InvalidPortError) Unwrap() []error { return []error{ErrReverseProxyPortInvalid, e.Reason} }

// kindError tags a store error with one of the sentinels above. errors.Is
// matches both the sentinel and the store error, but Error() is the store
// error's text alone, so an adapter that prints the error (the CLI) shows
// what it showed before this module existed, without a "domainops:" prefix.
type kindError struct {
	kind  error
	cause error
}

func (e *kindError) Error() string   { return e.cause.Error() }
func (e *kindError) Unwrap() []error { return []error{e.kind, e.cause} }

// PortDeps are the collaborators a reverse-proxy reservation needs.
type PortDeps struct {
	// Ports is the shared loopback-port allocator. Nil means reverse-proxy
	// domains are not available on this host.
	Ports repository.PortAllocationRepository
	// Agent answers the loopback-listener probe. Nil skips the probe. Callers
	// holding a concrete client pointer must leave this nil when the pointer is
	// nil: a nil *agent.Client in the interface is not a nil interface.
	Agent agent.AgentInterface
}

// ReserveReverseProxyPort reserves a loopback port for domainID. requested == 0
// draws the lowest free port from the reverse-proxy pool (GH #1175); any other
// value reserves exactly that port (GH #1401) after it passes the static policy
// and the system-uid probe.
//
// The probe fails open: when the agent is absent, errors, or answers garbage,
// the reservation proceeds on the static denylist alone, which stays the
// primary gate. A create must not hard-fail because the agent is momentarily
// unreachable.
func ReserveReverseProxyPort(ctx context.Context, d PortDeps, domainID string, requested int) (int, error) {
	if d.Ports == nil {
		return 0, ErrReverseProxyUnavailable
	}
	if requested == 0 {
		port, err := d.Ports.AllocateReverseProxy(ctx, domainID)
		if err != nil {
			return 0, &kindError{kind: ErrReverseProxyPortUnavailable, cause: err}
		}
		return port, nil
	}
	if err := repository.ValidateReverseProxyPort(requested); err != nil {
		return 0, &InvalidPortError{Reason: err}
	}
	if boundBySystemUID(ctx, d.Agent, requested) {
		return 0, ErrReverseProxyPortSystemBound
	}
	port, err := d.Ports.AllocateReverseProxySpecific(ctx, domainID, requested)
	switch {
	case errors.Is(err, repository.ErrPortInUse):
		return 0, &kindError{kind: ErrReverseProxyPortInUse, cause: err}
	case err != nil:
		return 0, &kindError{kind: ErrReverseProxyPortUnavailable, cause: err}
	}
	return port, nil
}

// boundBySystemUID reports whether port is LISTENing on loopback under a uid
// below the tenant floor. Any probe trouble reports false (fail open).
func boundBySystemUID(ctx context.Context, ag agent.AgentInterface, port int) bool {
	if ag == nil {
		return false
	}
	raw, err := ag.Call(ctx, loopbackListenerUIDCommand, map[string]any{"port": port})
	if err != nil {
		return false
	}
	var st struct {
		Bound bool `json:"bound"`
		UID   int  `json:"uid"`
	}
	if json.Unmarshal(raw, &st) != nil {
		return false
	}
	return st.Bound && st.UID >= 0 && st.UID < ReverseProxyTenantUIDFloor
}

// ReleaseReverseProxyPort hands domainID's reverse-proxy port back to the pool.
// It is compensation for a create that failed after the reservation, so it runs
// on a context that ignores the caller's cancellation: a client that
// disconnects mid-insert must not leak the port (AC4). Nil ports is a no-op.
func ReleaseReverseProxyPort(ctx context.Context, ports repository.PortAllocationRepository, domainID string) error {
	if ports == nil {
		return nil
	}
	return ports.Release(context.WithoutCancel(ctx), models.PortOwnerReverseProxy, domainID)
}

// DomainCreator is the slice of the domain repository PersistDomain needs.
type DomainCreator interface {
	Create(ctx context.Context, d *models.Domain) error
}

// PersistDomain inserts d. When the insert fails and d holds a reverse-proxy
// port, the port is released before the error is returned, so every failed
// create compensates its reservation (AC4). A unique-name conflict returns
// ErrDomainExists; any other failure returns ErrPersist. Both wrap the store
// error.
func PersistDomain(ctx context.Context, domains DomainCreator, ports repository.PortAllocationRepository, d *models.Domain) error {
	err := domains.Create(ctx, d)
	if err == nil {
		return nil
	}
	if d.ReverseProxyPort != 0 {
		_ = ReleaseReverseProxyPort(ctx, ports, d.ID)
	}
	if errors.Is(err, repository.ErrConflict) {
		return &kindError{kind: ErrDomainExists, cause: err}
	}
	return &kindError{kind: ErrPersist, cause: err}
}
