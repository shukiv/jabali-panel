package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ErrPortPoolExhausted — no free port in the requested pool for this
// (bind_interface, protocol). GH #1175.
var ErrPortPoolExhausted = errors.New("no free port in the allocation pool")

// ErrPortInUse — the specific port a tenant asked for (GH #1401) is already
// reserved by another allocation.
var ErrPortInUse = errors.New("requested port is already allocated")

// jabaliInfraPorts are fixed loopback ports jabali/system services bind. A
// tenant reverse-proxy target (GH #1401) must never point at these — else a
// public domain could be pointed at the panel or a system service (SSRF). Ports
// inside the allocator pools (10000-39999) and the FTP passive range
// (40000-40100) are blocked by range in ValidateReverseProxyPort; this set is
// the fixed service ports, listed EXPLICITLY so the denylist is complete on its
// own (a port that also falls in a blocked range, e.g. stalwart 18181, is
// listed here too — not left to range-coincidence).
// SECURITY-LOAD-BEARING: extend when a new jabali service binds a loopback port.
// (A follow-up adds an agent-side "is a non-tenant process bound here?" check
// that closes drift + the down-service race — see the #1401 PR.)
var jabaliInfraPorts = map[int]bool{
	3306:  true, // MariaDB
	4433:  true, // Kratos (legacy TCP listener)
	4434:  true, // Kratos (legacy TCP listener)
	5300:  true, // PowerDNS authoritative
	5432:  true, // PostgreSQL
	6060:  true, // CrowdSec metrics/pprof
	6379:  true, // Redis (unix-socket-only today; defensive)
	7422:  true, // CrowdSec AppSec
	8022:  true, // SSH (alternate)
	8080:  true, // Stalwart admin
	8081:  true, // CrowdSec LAPI
	8443:  true, // panel (nginx :8443)
	8446:  true, // Stalwart
	8462:  true, // jabali-mailhook
	18181: true, // Stalwart (also inside the docker pool range — explicit anyway)
}

// ValidateReverseProxyPort gates a tenant-chosen reverse-proxy target (GH
// #1401). Rejects privileged/out-of-range ports, the shared allocator pools
// (docker 10000-19999 / python 20000-29999 / reverse-proxy 30000-39999), the
// FTP passive range, and the fixed jabali-infra ports. Shared by the HTTP and
// CLI create paths so the policy has one home. 0 (auto-assign) is NOT passed
// here — the caller only validates an explicit tenant port.
func ValidateReverseProxyPort(port int) error {
	if port < 1024 || port > 65535 {
		return errors.New("port must be between 1024 and 65535")
	}
	// Infra denylist BEFORE the range blocks so a fixed service port gets the
	// accurate "system service" reason even when it also falls in a blocked
	// range (stalwart 18181 is inside the docker pool range).
	if jabaliInfraPorts[port] {
		return fmt.Errorf("port %d is used by a system service and can't be a reverse-proxy target", port)
	}
	if port >= 10000 && port <= 39999 {
		return errors.New("ports 10000-39999 are reserved for the panel's app allocator — pick a port outside that range")
	}
	if port >= 40000 && port <= 40100 {
		return errors.New("ports 40000-40100 are reserved for FTP — pick another")
	}
	return nil
}

// GH #1175 reverse-proxy port pool — a dedicated slice of port_allocations,
// disjoint from docker (10000-19999) + python (20000-29999) during the
// incremental adoption. Exported so both create paths (the HTTP handler and
// the `jabali domain create` CLI) draw from the identical pool.
const (
	ReverseProxyPoolMin   = 30000
	ReverseProxyPoolMax   = 39999
	ReverseProxyBindIface = "127.0.0.1"
	ReverseProxyProto     = "tcp"
)

// PortAllocationRepository is the shared loopback/host-port allocator every
// local-port consumer draws from (docker, python, reverse-proxy, …). GH #1175.
type PortAllocationRepository interface {
	// Allocate reserves the lowest free port in [poolMin, poolMax] for
	// (ownerKind, ownerID) on (bindInterface, protocol). Idempotent: if this
	// owner already holds a port for that interface+protocol, its existing port
	// is returned instead of a second reservation.
	Allocate(ctx context.Context, ownerKind, ownerID, bindInterface, protocol string, poolMin, poolMax int) (int, error)
	// AllocateReverseProxy reserves a loopback port for a GH #1175 reverse-proxy
	// domain (owner_kind='reverse_proxy') from the dedicated pool on
	// 127.0.0.1/tcp. Thin wrapper over Allocate so the pool bounds live in one
	// place and every create path draws from the same range. Idempotent per
	// domainID.
	AllocateReverseProxy(ctx context.Context, domainID string) (int, error)
	// AllocateReverseProxySpecific reserves the EXACT port a tenant chose (GH
	// #1401) for a reverse-proxy domain. Idempotent per domainID; returns
	// ErrPortInUse when that port is already allocated to another owner. The
	// caller validates the port is safe (validateReverseProxyPort) first.
	AllocateReverseProxySpecific(ctx context.Context, domainID string, port int) (int, error)
	// Release frees every port held by (ownerKind, ownerID). Safe to call when
	// the owner holds none.
	Release(ctx context.Context, ownerKind, ownerID string) error
	// PortForOwner returns the port held by (ownerKind, ownerID) on
	// (bindInterface, protocol), and ok=false when none is held.
	PortForOwner(ctx context.Context, ownerKind, ownerID, bindInterface, protocol string) (int, bool, error)
}

// lowestFreePort returns the lowest port in [poolMin, poolMax] not present in
// used, or 0 when the pool is exhausted. Pure so the allocation core is
// unit-tested without a DB.
func lowestFreePort(used []int, poolMin, poolMax int) int {
	usedSet := make(map[int]struct{}, len(used))
	for _, p := range used {
		usedSet[p] = struct{}{}
	}
	for p := poolMin; p <= poolMax; p++ {
		if _, taken := usedSet[p]; !taken {
			return p
		}
	}
	return 0
}

type portAllocationRepo struct{ db *gorm.DB }

func NewPortAllocationRepository(db *gorm.DB) PortAllocationRepository {
	return &portAllocationRepo{db: db}
}

func (r *portAllocationRepo) Allocate(ctx context.Context, ownerKind, ownerID, bindInterface, protocol string, poolMin, poolMax int) (int, error) {
	var port int
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Idempotent: an owner that already holds a port for this
		// interface+protocol gets the same one back (re-provisioning must not
		// leak a second port).
		var existing models.PortAllocation
		e := tx.Where("owner_kind = ? AND owner_id = ? AND bind_interface = ? AND protocol = ?",
			ownerKind, ownerID, bindInterface, protocol).First(&existing).Error
		if e == nil {
			port = int(existing.Port)
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		// Lock the (interface, protocol) rows so a concurrent allocation can't
		// pick the same lowest-free gap; the UNIQUE(bind_interface, port,
		// protocol) is the final backstop.
		var used []int
		if err := tx.Model(&models.PortAllocation{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("bind_interface = ? AND protocol = ?", bindInterface, protocol).
			Order("port ASC").
			Pluck("port", &used).Error; err != nil {
			return err
		}
		chosen := lowestFreePort(used, poolMin, poolMax)
		if chosen == 0 {
			return ErrPortPoolExhausted
		}
		row := &models.PortAllocation{
			ID:            ids.NewULID(),
			Port:          uint32(chosen),
			BindInterface: bindInterface,
			Protocol:      protocol,
			OwnerKind:     ownerKind,
			OwnerID:       ownerID,
			CreatedAt:     time.Now().UTC(),
		}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		port = chosen
		return nil
	})
	if err != nil {
		return 0, err
	}
	return port, nil
}

func (r *portAllocationRepo) AllocateReverseProxy(ctx context.Context, domainID string) (int, error) {
	return r.Allocate(ctx, models.PortOwnerReverseProxy, domainID,
		ReverseProxyBindIface, ReverseProxyProto, ReverseProxyPoolMin, ReverseProxyPoolMax)
}

// AllocateReverseProxySpecific reserves the EXACT port (GH #1401). Idempotent
// per owner (a re-provision returns the held port); ErrPortInUse when the port
// belongs to another owner. Locks the (interface, protocol) rows so a
// concurrent claim can't double-reserve; the UNIQUE(bind_interface, port,
// protocol) index is the final backstop.
func (r *portAllocationRepo) AllocateReverseProxySpecific(ctx context.Context, domainID string, port int) (int, error) {
	var out int
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing models.PortAllocation
		e := tx.Where("owner_kind = ? AND owner_id = ? AND bind_interface = ? AND protocol = ?",
			models.PortOwnerReverseProxy, domainID, ReverseProxyBindIface, ReverseProxyProto).First(&existing).Error
		if e == nil {
			out = int(existing.Port)
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		var taken int64
		if err := tx.Model(&models.PortAllocation{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("bind_interface = ? AND protocol = ? AND port = ?", ReverseProxyBindIface, ReverseProxyProto, port).
			Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return ErrPortInUse
		}
		row := &models.PortAllocation{
			ID:            ids.NewULID(),
			Port:          uint32(port),
			BindInterface: ReverseProxyBindIface,
			Protocol:      ReverseProxyProto,
			OwnerKind:     models.PortOwnerReverseProxy,
			OwnerID:       domainID,
			CreatedAt:     time.Now().UTC(),
		}
		if err := tx.Create(row).Error; err != nil {
			return ErrPortInUse // UNIQUE backstop on a concurrent claim
		}
		out = port
		return nil
	})
	if err != nil {
		return 0, err
	}
	return out, nil
}

func (r *portAllocationRepo) Release(ctx context.Context, ownerKind, ownerID string) error {
	return r.db.WithContext(ctx).
		Where("owner_kind = ? AND owner_id = ?", ownerKind, ownerID).
		Delete(&models.PortAllocation{}).Error
}

func (r *portAllocationRepo) PortForOwner(ctx context.Context, ownerKind, ownerID, bindInterface, protocol string) (int, bool, error) {
	var row models.PortAllocation
	err := r.db.WithContext(ctx).
		Where("owner_kind = ? AND owner_id = ? AND bind_interface = ? AND protocol = ?",
			ownerKind, ownerID, bindInterface, protocol).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return int(row.Port), true, nil
}
