package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// PolicyForReconcile is the joined view consumed by the reconciler — one
// row per Linux user, carrying everything the nft template renderer
// needs in a single round-trip. Username + UID come from the users
// table; the rest from user_egress_policies.
type PolicyForReconcile struct {
	UserID            string
	Username          string
	UID               *uint
	State             string
	AllowedExtra      []models.EgressDestination
	LearningStartedAt *time.Time
	// Per-package egress allowances (GH #1798), folded in via a LEFT JOIN on
	// the user's hosting_package. A NULL package (admin account, or a package
	// row that predates migration 000304) COALESCEs to the DENY defaults —
	// false / "" — so a missing package never grants a privileged allowance.
	EgressSSHOut      bool
	EgressSSHOutCIDRs string
	EgressICMP        bool
}

// UserEgressPolicyRepository owns user_egress_policies. Admin handlers
// call Upsert; the reconciler calls ListAllForReconcile + BumpDropCount.
// EnsureDefault is the idempotent insert-or-noop used by user-create
// hooks so every Linux user has exactly one row at all times.
type UserEgressPolicyRepository interface {
	Get(ctx context.Context, userID string) (*models.UserEgressPolicy, error)
	Upsert(ctx context.Context, p *models.UserEgressPolicy) error
	EnsureDefault(ctx context.Context, userID, defaultState string) error
	List(ctx context.Context) ([]models.UserEgressPolicy, error)
	ListAllForReconcile(ctx context.Context) ([]PolicyForReconcile, error)
	SetDropCount(ctx context.Context, userID string, count uint64, at time.Time) error
	StateCounts(ctx context.Context) (map[string]uint, error)
	ListMatureLearning(ctx context.Context, age time.Duration) ([]models.UserEgressPolicy, error)
}

type userEgressPolicyRepo struct{ db *gorm.DB }

// NewUserEgressPolicyRepository returns a GORM-backed repo.
func NewUserEgressPolicyRepository(db *gorm.DB) UserEgressPolicyRepository {
	return &userEgressPolicyRepo{db: db}
}

// Get returns the policy for one user, or ErrNotFound if no row exists.
func (r *userEgressPolicyRepo) Get(ctx context.Context, userID string) (*models.UserEgressPolicy, error) {
	var row models.UserEgressPolicy
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).First(&row).Error; err != nil {
		return nil, translate(err)
	}
	return &row, nil
}

// Upsert writes a full policy row, replacing every column except the
// drop-counter pair (which only the reconciler updates). Used by admin
// PUT /admin/users/:id/egress and user-request approval handlers.
func (r *userEgressPolicyRepo) Upsert(ctx context.Context, p *models.UserEgressPolicy) error {
	if len(p.AllowedExtra) == 0 {
		p.AllowedExtra = json.RawMessage("[]")
	}
	// learning_started_at must be set the first time a row enters
	// 'learning' so the Step 8 auto-flip timer can mature it. We compute
	// it here rather than in a migration trigger to keep migrations
	// schema-only (memory: feedback_migration_data_seed_ordering).
	if p.State == models.UserEgressStateLearning && p.LearningStartedAt == nil {
		now := time.Now().UTC()
		p.LearningStartedAt = &now
	}
	if p.State != models.UserEgressStateLearning {
		p.LearningStartedAt = nil
	}
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"state", "allowed_extra", "learning_started_at",
				"updated_at", "updated_by",
			}),
		}).
		Create(p).Error
	if err != nil {
		return translate(err)
	}
	return nil
}

// EnsureDefault inserts an empty-allowlist row in the requested state
// if no row exists for the user. No-op if a row already exists. Called
// from the user-create hook so brand-new users join the firewall in
// the right mode without operator intervention.
func (r *userEgressPolicyRepo) EnsureDefault(ctx context.Context, userID, defaultState string) error {
	if defaultState == "" {
		defaultState = models.UserEgressStateEnforced
	}
	var learningStartedAt *time.Time
	if defaultState == models.UserEgressStateLearning {
		now := time.Now().UTC()
		learningStartedAt = &now
	}
	row := models.UserEgressPolicy{
		UserID:            userID,
		State:             defaultState,
		AllowedExtra:      json.RawMessage("[]"),
		LearningStartedAt: learningStartedAt,
	}
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&row).Error
	if err != nil {
		return translate(err)
	}
	return nil
}

// List returns every row, ordered by user_id for a stable iteration.
func (r *userEgressPolicyRepo) List(ctx context.Context) ([]models.UserEgressPolicy, error) {
	var rows []models.UserEgressPolicy
	if err := r.db.WithContext(ctx).Order("user_id ASC").Find(&rows).Error; err != nil {
		return nil, translate(err)
	}
	return rows, nil
}

// ListAllForReconcile joins user_egress_policies against users to
// resolve username + uid in one query. The reconciler renders the
// nftables file directly from the result. UID is nullable because the
// users table allows nil uid for users who pre-exist the M18 slice
// migration; we skip those at render time (the agent handles).
func (r *userEgressPolicyRepo) ListAllForReconcile(ctx context.Context) ([]PolicyForReconcile, error) {
	type joined struct {
		UserID            string
		Username          string
		UID               *uint
		State             string
		AllowedExtra      []byte
		LearningStartedAt *time.Time
		EgressSSHOut      bool
		EgressSSHOutCIDRs string
		EgressICMP        bool
	}
	var rows []joined
	// GH #1798: LEFT JOIN the user's hosting_package for its egress allowances.
	// LEFT (not INNER) so admin/NULL-package users keep their policy row; the
	// COALESCEs turn a missing package into the DENY defaults (false / "").
	err := r.db.WithContext(ctx).
		Table("user_egress_policies AS p").
		Select(`p.user_id,
			u.username AS username,
			u.linux_uid AS uid,
			p.state,
			p.allowed_extra,
			p.learning_started_at,
			COALESCE(hp.egress_ssh_out, 0) AS egress_ssh_out,
			COALESCE(hp.egress_ssh_out_cidrs, '') AS egress_ssh_out_cidrs,
			COALESCE(hp.egress_icmp, 0) AS egress_icmp`).
		Joins("INNER JOIN users u ON u.id = p.user_id").
		Joins("LEFT JOIN hosting_packages hp ON hp.id = u.package_id").
		Order("p.user_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, translate(err)
	}
	out := make([]PolicyForReconcile, 0, len(rows))
	for _, r := range rows {
		var extras []models.EgressDestination
		if len(r.AllowedExtra) > 0 && string(r.AllowedExtra) != "null" {
			if err := json.Unmarshal(r.AllowedExtra, &extras); err != nil {
				return nil, err
			}
		}
		if extras == nil {
			extras = []models.EgressDestination{}
		}
		out = append(out, PolicyForReconcile{
			UserID:            r.UserID,
			Username:          r.Username,
			UID:               r.UID,
			State:             r.State,
			AllowedExtra:      extras,
			LearningStartedAt: r.LearningStartedAt,
			EgressSSHOut:      r.EgressSSHOut,
			EgressSSHOutCIDRs: r.EgressSSHOutCIDRs,
			EgressICMP:        r.EgressICMP,
		})
	}
	return out, nil
}

// SetDropCount writes the latest 24h-window drop count + observation
// timestamp. The reconciler reads nft counters every tick, diffs vs
// prior tick, and rolls into a 24h sum which it then persists here.
// drop_count_24h doubles as the M14 burst-source signal source.
func (r *userEgressPolicyRepo) SetDropCount(ctx context.Context, userID string, count uint64, at time.Time) error {
	res := r.db.WithContext(ctx).
		Model(&models.UserEgressPolicy{}).
		Where("user_id = ?", userID).
		Updates(map[string]any{
			"drop_count_24h": count,
			"drop_count_at":  at.UTC(),
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// StateCounts returns a map of state -> row count, used by the admin
// dashboard widget (Step 6) without a second round-trip.
func (r *userEgressPolicyRepo) StateCounts(ctx context.Context) (map[string]uint, error) {
	type row struct {
		State string
		N     uint
	}
	var rows []row
	err := r.db.WithContext(ctx).
		Model(&models.UserEgressPolicy{}).
		Select("state, COUNT(*) AS n").
		Group("state").
		Scan(&rows).Error
	if err != nil {
		return nil, translate(err)
	}
	out := map[string]uint{
		models.UserEgressStateOff:      0,
		models.UserEgressStateLearning: 0,
		models.UserEgressStateEnforced: 0,
	}
	for _, r := range rows {
		out[r.State] = r.N
	}
	return out, nil
}

// ListMatureLearning returns rows that have been in 'learning' for at
// least the given age. Step 8's `jabali per-user-egress flip-mature`
// CLI consumes this and flips them to enforced unless the operator-pin
// file /etc/jabali/per-user-egress.mode == "learning" is set.
func (r *userEgressPolicyRepo) ListMatureLearning(ctx context.Context, age time.Duration) ([]models.UserEgressPolicy, error) {
	if age <= 0 {
		return nil, errors.New("ListMatureLearning: age must be positive")
	}
	cutoff := time.Now().UTC().Add(-age)
	var rows []models.UserEgressPolicy
	err := r.db.WithContext(ctx).
		Where("state = ? AND learning_started_at IS NOT NULL AND learning_started_at <= ?",
			models.UserEgressStateLearning, cutoff).
		Find(&rows).Error
	if err != nil {
		return nil, translate(err)
	}
	return rows, nil
}
