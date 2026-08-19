package models

import "time"

// Owner kinds for a PortAllocation. The allocator is shared, so each consumer
// tags its rows so releases + lookups stay scoped to it. GH #1175.
const (
	PortOwnerReverseProxy = "reverse_proxy"
	PortOwnerDockerApp    = "docker_app"
	PortOwnerPythonApp    = "python_app"
)

// PortAllocation is one loopback/host port reserved for one owner on one
// (bind_interface, protocol). The shared registry every local-port consumer
// draws from (GH #1175). The UNIQUE on (bind_interface, port, protocol) is what
// makes the lowest-free allocator race-safe.
type PortAllocation struct {
	ID            string    `gorm:"column:id;type:char(26);primaryKey" json:"id"`
	Port          uint32    `gorm:"column:port;not null" json:"port"`
	BindInterface string    `gorm:"column:bind_interface;type:varchar(64);not null;default:'127.0.0.1'" json:"bind_interface"`
	Protocol      string    `gorm:"column:protocol;type:varchar(8);not null;default:'tcp'" json:"protocol"`
	OwnerKind     string    `gorm:"column:owner_kind;type:varchar(32);not null" json:"owner_kind"`
	OwnerID       string    `gorm:"column:owner_id;type:varchar(64);not null" json:"owner_id"`
	CreatedAt     time.Time `gorm:"column:created_at;not null" json:"created_at"`
}

// TableName pins the table (GORM would otherwise pluralize to "port_allocations"
// — which is correct here, but pin it so a future rename can't drift).
func (PortAllocation) TableName() string { return "port_allocations" }
