package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/drsync"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1169 (deferred #331 blueprint step 5) — admin DR status endpoint.
//
// Freshness of a DR standby was visible only via `jabali dr status` ON the
// standby. This exposes the same picture to the admin UI (role, peer, last-sync
// age, freshness) so an operator sees a stalling replica from the panel.

// DRStatusConfig wires the read repos the endpoint needs.
type DRStatusConfig struct {
	Settings     repository.ServerSettingsRepository
	Destinations repository.BackupDestinationRepository
}

// RegisterDRStatusRoute mounts GET /admin/dr/status on an admin-gated group.
func RegisterDRStatusRoute(admin *gin.RouterGroup, cfg DRStatusConfig) {
	if cfg.Settings == nil {
		return
	}
	admin.GET("/dr/status", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		s, err := cfg.Settings.Get(ctx)
		if err != nil || s == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		c.JSON(http.StatusOK, buildDRStatus(ctx, s, cfg.Destinations, time.Now()))
	})
}

// drStatusResponse is the admin DR status card's wire shape.
type drStatusResponse struct {
	Role            string `json:"role"`
	IsStandby       bool   `json:"is_standby"`
	Paired          bool   `json:"paired"`
	Peer            string `json:"peer"`
	DestinationID   string `json:"destination_id"`
	DestinationName string `json:"destination_name"`
	PairedAt        string `json:"paired_at,omitempty"`
	LastSyncAt      string `json:"last_sync_at,omitempty"`
	LastSyncStatus  string `json:"last_sync_status,omitempty"`
	LastSnapshotID  string `json:"last_snapshot_id,omitempty"`
	LastSyncError   string `json:"last_sync_error,omitempty"`
	// SyncAgeSeconds is how long since the last applied snapshot (or, before the
	// first, since pairing). -1 when there is no baseline (unpaired primary).
	SyncAgeSeconds  int64 `json:"sync_age_seconds"`
	StaleThresholdS int64 `json:"stale_threshold_seconds"`
	// Stalled mirrors the dr.sync.stalled alert: age past the threshold on a
	// standby. Always false on a primary.
	Stalled bool `json:"stalled"`
}

// buildDRStatus assembles the response. Pure but for the destination-name
// lookup; `now` is injected so the age math is unit-testable.
func buildDRStatus(ctx context.Context, s *models.ServerSettings, dests repository.BackupDestinationRepository, now time.Time) drStatusResponse {
	role := s.ServerRole
	if role == "" {
		role = models.ServerRolePrimary
	}
	destID := ""
	if s.DRDestinationID != nil {
		destID = *s.DRDestinationID
	}
	// Same staleness threshold the dr.sync.stalled alert uses (drsync's
	// DefaultStalledCycles × DefaultInterval) so the card's badge agrees with
	// the notification. 5 cycles is kept in step with drsync.DefaultStalledCycles.
	const staleCycles = 5
	threshold := time.Duration(staleCycles) * drsync.DefaultInterval
	out := drStatusResponse{
		Role:            role,
		IsStandby:       s.IsStandby(),
		Peer:            s.DRPeerLabel,
		DestinationID:   destID,
		LastSyncStatus:  s.DRLastSyncStatus,
		LastSnapshotID:  s.DRLastSnapshotID,
		LastSyncError:   s.DRLastSyncError,
		SyncAgeSeconds:  -1,
		StaleThresholdS: int64(threshold / time.Second),
	}
	out.Paired = s.DRPairedAt != nil && out.DestinationID != ""
	if s.DRPairedAt != nil {
		out.PairedAt = s.DRPairedAt.UTC().Format(time.RFC3339)
	}
	if s.DRLastSyncAt != nil {
		out.LastSyncAt = s.DRLastSyncAt.UTC().Format(time.RFC3339)
	}

	// Freshness baseline: last applied snapshot, else pairing time.
	var baseline *time.Time
	if s.DRLastSyncAt != nil {
		baseline = s.DRLastSyncAt
	} else if s.DRPairedAt != nil {
		baseline = s.DRPairedAt
	}
	if baseline != nil {
		age := now.Sub(*baseline)
		if age < 0 {
			age = 0
		}
		out.SyncAgeSeconds = int64(age / time.Second)
		// Only a standby can be "stalled" — a primary's loop is inert.
		out.Stalled = out.IsStandby && age > threshold
	}

	if out.DestinationID != "" && dests != nil {
		if d, derr := dests.Get(ctx, out.DestinationID); derr == nil && d != nil {
			out.DestinationName = d.Name
		}
	}
	return out
}
