package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ServerSettingsRepository is the interface for accessing the single-row
// server_settings table. Implementations must enforce that only row id=1
// exists.
type ServerSettingsRepository interface {
	Get(ctx context.Context) (*models.ServerSettings, error)
	Upsert(ctx context.Context, s *models.ServerSettings) error

	// EnsureVAPID is the first-boot VAPID keypair seed. Generates a
	// P-256 keypair via webpush.GenerateVAPIDKeys if vapid_public_key
	// is currently NULL on the settings row, then writes the public/
	// private/subject trio in a single UPDATE.
	//
	// Idempotent: a non-NULL public_key on entry skips generation and
	// returns generated=false. Partial state (public_key NULL while
	// private_key or subject is set) is treated as corruption and
	// returned as error — operator must intervene rather than have us
	// silently regenerate into a half-written row.
	//
	// hostname is the installation's canonical hostname used to build
	// the mailto: subject (e.g. "mailto:admin@panel.example.com").
	// Empty hostname falls back to "mailto:admin@localhost" so first
	// boot on a not-yet-configured host still succeeds.
	EnsureVAPID(ctx context.Context, hostname string) (generated bool, err error)

	// SetDigestLastSent records the UTC day (YYYY-MM-DD) the daily digest
	// last fired for (GH #840). Targeted single-column UPDATE so it can
	// never clobber a concurrent admin settings save.
	SetDigestLastSent(ctx context.Context, day string) error

	// RecordDRSync records the outcome of one drsync tick on a standby (GH
	// #331 Step 2): the timestamp, status (models.DRSyncStatus*), and error
	// string. snapshotID is written only when non-empty (i.e. a snapshot was
	// actually applied) so a failed/waiting tick doesn't erase the last-known
	// applied snapshot. Targeted UPDATE so it never clobbers a concurrent
	// settings save.
	RecordDRSync(ctx context.Context, snapshotID, status, syncErr string) error

	// ReassertDRPairing re-stamps this box's standby identity (GH #331): an
	// applied drsync restore loads the PRIMARY's panel DB wholesale, which
	// replaces server_settings with a row that says role=primary and carries
	// no pairing — without re-asserting, a standby self-demotes after its
	// first successful sync. Column-targeted UPDATE of exactly the four
	// identity columns so everything else the restore replicated is kept.
	ReassertDRPairing(ctx context.Context, destinationID, peerLabel string, pairedAt *time.Time) error
}

type serverSettingsRepo struct{ db *gorm.DB }

// NewServerSettingsRepository returns a ServerSettingsRepository backed by
// the given GORM handle.
func NewServerSettingsRepository(db *gorm.DB) ServerSettingsRepository {
	return &serverSettingsRepo{db: db}
}

func (r *serverSettingsRepo) Get(ctx context.Context) (*models.ServerSettings, error) {
	var s models.ServerSettings
	if err := r.db.WithContext(ctx).First(&s, 1).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

// Upsert writes the row, creating it if missing. Forces ID=1.
// Uses explicit exists-check + Create/Updates instead of Save() because
// GORM's Save with the `uint8 primaryKey default:1` tag on MySQL 8 /
// MariaDB 10.11 generates SQL that lists the `id` column twice in the
// INSERT ... ON DUPLICATE KEY UPDATE clause, triggering error 1110
// ("Column 'id' specified twice").
func (r *serverSettingsRepo) Upsert(ctx context.Context, s *models.ServerSettings) error {
	s.ID = 1
	s.UpdatedAt = time.Now().UTC()

	var existing models.ServerSettings
	err := r.db.WithContext(ctx).First(&existing, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.WithContext(ctx).Create(s).Error
	}
	if err != nil {
		return err
	}
	// Select("*") forces all columns to be updated — including zero
	// values — so a caller that intentionally clears a field (e.g.
	// admin_email to "") gets the clear persisted. Omit("id") keeps
	// the primary key out of the SET clause.
	return r.db.WithContext(ctx).Model(&existing).Select("*").Omit("id").Updates(s).Error
}

func (r *serverSettingsRepo) EnsureVAPID(ctx context.Context, hostname string) (bool, error) {
	var existing models.ServerSettings
	err := r.db.WithContext(ctx).First(&existing, 1).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrNotFound
		}
		return false, err
	}

	// Already seeded — caller is idempotent.
	if existing.VAPIDPublicKey != nil && *existing.VAPIDPublicKey != "" {
		return false, nil
	}

	// Partial state guard — the only safe way to have the other two
	// set is a half-run of a previous EnsureVAPID. Refuse to
	// regenerate blindly; let the operator decide (the docs runbook
	// walks through the recovery: NULL-out all three, restart).
	if (existing.VAPIDPrivateKey != nil && *existing.VAPIDPrivateKey != "") ||
		(existing.VAPIDSubject != nil && *existing.VAPIDSubject != "") {
		return false, errors.New("server_settings: partial VAPID state — public_key NULL but private_key or subject set; operator must NULL all three before retry")
	}

	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return false, fmt.Errorf("generate VAPID keys: %w", err)
	}

	subjectHost := hostname
	if subjectHost == "" {
		subjectHost = "localhost"
	}
	subject := "mailto:admin@" + subjectHost

	now := time.Now().UTC()
	res := r.db.WithContext(ctx).
		Model(&models.ServerSettings{}).
		Where("id = ?", 1).
		Updates(map[string]any{
			"vapid_public_key":  pub,
			"vapid_private_key": priv,
			"vapid_subject":     subject,
			"updated_at":        now,
		})
	if res.Error != nil {
		return false, fmt.Errorf("persist VAPID keys: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return false, ErrNotFound
	}
	return true, nil
}

// SetDigestLastSent — see the interface doc. The settings table is a
// single row; an empty WHERE is intentional.
func (r *serverSettingsRepo) SetDigestLastSent(ctx context.Context, day string) error {
	return r.db.WithContext(ctx).
		Model(&models.ServerSettings{}).
		Where("1 = 1").
		Update("digest_last_sent_date", day).Error
}

// RecordDRSync — see the interface doc. Empty WHERE targets the single
// settings row. syncErr is truncated to the column width (1024) so a long
// restic error string can never fail the UPDATE.
func (r *serverSettingsRepo) RecordDRSync(ctx context.Context, snapshotID, status, syncErr string) error {
	const maxErr = 1024
	if len(syncErr) > maxErr {
		syncErr = syncErr[:maxErr]
	}
	updates := map[string]any{
		"dr_last_sync_at":     time.Now().UTC(),
		"dr_last_sync_status": status,
		"dr_last_sync_error":  syncErr,
	}
	if snapshotID != "" {
		updates["dr_last_snapshot_id"] = snapshotID
	}
	return r.db.WithContext(ctx).
		Model(&models.ServerSettings{}).
		Where("1 = 1").
		Updates(updates).Error
}

// ReassertDRPairing — see the interface doc. Empty WHERE targets the single
// settings row; a map (not struct) so the nil pairedAt and empty peerLabel
// are written, not skipped as zero values.
func (r *serverSettingsRepo) ReassertDRPairing(ctx context.Context, destinationID, peerLabel string, pairedAt *time.Time) error {
	return r.db.WithContext(ctx).
		Model(&models.ServerSettings{}).
		Where("1 = 1").
		Updates(map[string]any{
			"server_role":       models.ServerRoleStandby,
			"dr_destination_id": destinationID,
			"dr_peer_label":     peerLabel,
			"dr_paired_at":      pairedAt,
		}).Error
}
