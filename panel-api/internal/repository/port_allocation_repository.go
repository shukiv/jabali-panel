package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ErrPortPoolExhausted — no free port in the requested pool for this
// (bind_interface, protocol). GH #1175.
var ErrPortPoolExhausted = errors.New("no free port in the allocation pool")

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
