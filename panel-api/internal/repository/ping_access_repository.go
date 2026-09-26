package repository

import (
	"context"

	"gorm.io/gorm"
)

// PingAccessRepository lists the tenants allowed to ping from their shell
// (GH #1798): users whose hosting package has egress_icmp. The reconciler
// sends the list to the Agent's user.ping_access.apply, which makes it the
// exact membership of the jabali-ping group.
type PingAccessRepository interface {
	ListPingAllowedUsernames(ctx context.Context) ([]string, error)
}

type pingAccessRepo struct{ db *gorm.DB }

// NewPingAccessRepository returns a GORM-backed repo.
func NewPingAccessRepository(db *gorm.DB) PingAccessRepository {
	return &pingAccessRepo{db: db}
}

// ListPingAllowedUsernames returns the usernames, sorted. The INNER JOIN is
// the deny path: a user without a package is not listed (a privileged
// feature is denied to a NULL package, GH #282), and neither is an admin
// account, which has no username.
func (r *pingAccessRepo) ListPingAllowedUsernames(ctx context.Context) ([]string, error) {
	var names []string
	err := r.db.WithContext(ctx).
		Table("users AS u").
		Joins("INNER JOIN hosting_packages hp ON hp.id = u.package_id").
		Where("hp.egress_icmp = ?", true).
		Where("u.username IS NOT NULL AND u.username <> ''").
		Order("u.username ASC").
		Pluck("u.username", &names).Error
	if err != nil {
		return nil, translate(err)
	}
	return names, nil
}
