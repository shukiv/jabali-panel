// Package repository — BackupJobRepository owns the backup_jobs table.
// Step 1 lands minimal CRUD: Create on schedule, Get for status reads,
// MarkStatus for state transitions, ListForUser for the UI list page.
// Step 6 wires the orchestrator on top; Step 8 wires the REST handlers.
//
// Schema: migration 000084 (M30 / ADR-0075).
package repository

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// BackupJobRepository is the narrow interface every consumer (orchestrator,
// REST handler, retention timer) depends on. New methods land here as the
// surface grows; do not widen ListForUser into a generic ListOptions until
// a real second caller appears.
type BackupJobRepository interface {
	Create(ctx context.Context, job *models.BackupJob) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*models.BackupJob, error)
	ListForUser(ctx context.Context, userID string, limit, offset int) ([]models.BackupJob, int64, error)
	// OldestAccountBackupForUser returns the caller's OLDEST account_backup job
	// (created_at ASC) for auto-prune retention (GH #454), or ErrNotFound when
	// they have none. Owner-scoped by user_id; the prune caller never passes a
	// body id.
	OldestAccountBackupForUser(ctx context.Context, userID string) (*models.BackupJob, error)
	ListAll(ctx context.Context, limit, offset int) ([]models.BackupJob, int64, error)
	MarkStarted(ctx context.Context, id string) error
	MarkFinished(ctx context.Context, id, status string, snapshotID, parentSnapshot string, bytesAdded, bytesTotal uint64, manifest, warnings json.RawMessage, errText string) error

	// Queue helpers — used by the in-process dispatcher (M30.1
	// follow-up). CountByStatus + ListQueuedOldest let it cap
	// dispatches at server_settings.backup_max_concurrent_jobs.
	CountByStatus(ctx context.Context, status string) (int64, error)
	ListQueuedOldest(ctx context.Context, limit int) ([]models.BackupJob, error)
	// ListRunning is the finalizer's worklist. It must query status
	// directly: paging the newest N of ALL statuses and filtering in Go
	// loses the oldest-created running jobs of a big fan-out, which then
	// wedge the queue permanently. See the method comment.
	ListRunning(ctx context.Context, limit int) ([]models.BackupJob, error)
	// CountForUser counts a user's jobs for the retention cap without
	// materialising rows.
	CountForUser(ctx context.Context, userID string) (int64, error)

	// Run grouping — admin UI rolls scheduler-fired jobs under one
	// header per run_id. ListRuns aggregates jobs that share a run_id;
	// ListByRun fetches all child jobs for an expanded row;
	// ListManual streams jobs with run_id NULL (manual creates) so
	// the UI can interleave them with grouped runs.
	ListRuns(ctx context.Context, limit, offset int) ([]BackupRunSummary, int64, error)
	ListByRun(ctx context.Context, runID string) ([]models.BackupJob, error)
	ListManual(ctx context.Context, limit, offset int) ([]models.BackupJob, int64, error)

	// ListByStatusSince returns rows whose status matches AND
	// finished_at >= since. Used by the M14 backup_fail event source
	// to drain newly-failed jobs into the notification dispatcher.
	// Limit caps the scan so a wave of failures from a stuck backend
	// can't blow up memory.
	ListByStatusSince(ctx context.Context, status string, since time.Time, limit int) ([]models.BackupJob, error)
}

// BackupRunSummary aggregates one logical scheduler tick (run_id) into
// the columns the admin UI needs for its parent rows. Per-job detail
// loads on demand via ListByRun.
type BackupRunSummary struct {
	RunID         string    `json:"run_id"`
	ScheduleID    *string   `json:"schedule_id,omitempty"`
	Kind          string    `json:"kind"`
	Content       string    `json:"content"`
	HasAccounts   bool      `json:"has_accounts"`
	// Accounts is the number of per-account snapshots in the run (GH #502) — so
	// the UI can label a Full Server run "system + N accounts" on the collapsed
	// row instead of making the admin expand it to count.
	Accounts      int       `json:"accounts"`
	Total         int       `json:"total"`
	Succeeded     int       `json:"succeeded"`
	Failed        int       `json:"failed"`
	Running       int       `json:"running"`
	Queued        int       `json:"queued"`
	Cancelled     int       `json:"cancelled"`
	Partial       int       `json:"partial"`
	BytesAdded    uint64    `json:"bytes_added"`
	BytesTotal    uint64    `json:"bytes_total"`
	StartedAt     time.Time `json:"started_at"`
	LatestUpdated time.Time `json:"latest_updated"`
}

type backupJobRepo struct{ db *gorm.DB }

// NewBackupJobRepository returns a GORM-backed BackupJobRepository.
func NewBackupJobRepository(db *gorm.DB) BackupJobRepository {
	return &backupJobRepo{db: db}
}

func (r *backupJobRepo) Create(ctx context.Context, job *models.BackupJob) error {
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	if job.Status == "" {
		job.Status = models.BackupJobStatusQueued
	}
	if err := r.db.WithContext(ctx).Create(job).Error; err != nil {
		return translate(err)
	}
	return nil
}

func (r *backupJobRepo) Get(ctx context.Context, id string) (*models.BackupJob, error) {
	var out models.BackupJob
	if err := r.db.WithContext(ctx).First(&out, "id = ?", id).Error; err != nil {
		return nil, translate(err)
	}
	return &out, nil
}

// Delete removes a backup job row. The restic snapshots are forgotten
// separately by the agent (backup.forget) before this is called.
func (r *backupJobRepo) Delete(ctx context.Context, id string) error {
	return translate(r.db.WithContext(ctx).Delete(&models.BackupJob{}, "id = ?", id).Error)
}

func (r *backupJobRepo) ListForUser(ctx context.Context, userID string, limit, offset int) ([]models.BackupJob, int64, error) {
	return r.list(ctx, "user_id = ?", []any{userID}, limit, offset)
}

func (r *backupJobRepo) OldestAccountBackupForUser(ctx context.Context, userID string) (*models.BackupJob, error) {
	var out models.BackupJob
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND kind = ?", userID, models.BackupJobKindAccountBackup).
		Order("created_at ASC").
		First(&out).Error
	if err != nil {
		return nil, translate(err)
	}
	return &out, nil
}

func (r *backupJobRepo) ListAll(ctx context.Context, limit, offset int) ([]models.BackupJob, int64, error) {
	return r.list(ctx, "", nil, limit, offset)
}

func (r *backupJobRepo) list(ctx context.Context, where string, args []any, limit, offset int) ([]models.BackupJob, int64, error) {
	var (
		out   []models.BackupJob
		total int64
	)
	q := r.db.WithContext(ctx).Model(&models.BackupJob{})
	if where != "" {
		q = q.Where(where, args...)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, translate(err)
	}
	if limit <= 0 {
		limit = 50
	}
	if err := q.Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&out).Error; err != nil {
		return nil, 0, translate(err)
	}
	return out, total, nil
}

func (r *backupJobRepo) CountByStatus(ctx context.Context, status string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&models.BackupJob{}).
		Where("status = ?", status).
		Count(&n).Error
	if err != nil {
		return 0, translate(err)
	}
	return n, nil
}

func (r *backupJobRepo) ListRuns(ctx context.Context, limit, offset int) ([]BackupRunSummary, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	var total int64
	if err := r.db.WithContext(ctx).
		Raw(`SELECT COUNT(DISTINCT run_id) FROM backup_jobs WHERE run_id IS NOT NULL`).
		Scan(&total).Error; err != nil {
		return nil, 0, translate(err)
	}
	type row struct {
		RunID         string
		ScheduleID    *string
		Kind          string
		Content       string
		HasAccounts   bool
		Accounts      int
		Total         int
		Succeeded     int
		Failed        int
		Running       int
		Queued        int
		Cancelled     int
		Partial       int
		BytesAdded    uint64
		BytesTotal    uint64
		StartedAt     time.Time
		LatestUpdated time.Time
	}
	var rows []row
	err := r.db.WithContext(ctx).Raw(`
SELECT run_id                                                             AS run_id,
       MAX(schedule_id)                                                   AS schedule_id,
       MAX(kind)                                                          AS kind,
       CASE WHEN COUNT(DISTINCT content) = 1 THEN MAX(content)
            ELSE 'full' END                                              AS content,
       SUM(kind = 'account_backup') > 0                                  AS has_accounts,
       SUM(kind = 'account_backup')                                      AS accounts,
       COUNT(*)                                                           AS total,
       SUM(status = 'succeeded')                                          AS succeeded,
       SUM(status = 'failed')                                             AS failed,
       SUM(status = 'running')                                            AS running,
       SUM(status = 'queued')                                             AS queued,
       SUM(status = 'cancelled')                                          AS cancelled,
       SUM(status = 'partial')                                            AS partial,
       SUM(bytes_added)                                                   AS bytes_added,
       SUM(bytes_total)                                                   AS bytes_total,
       MIN(created_at)                                                    AS started_at,
       GREATEST(MAX(COALESCE(finished_at, '1970-01-01')),
                MAX(COALESCE(started_at,  '1970-01-01')),
                MAX(created_at))                                          AS latest_updated
  FROM backup_jobs
 WHERE run_id IS NOT NULL
 GROUP BY run_id
 ORDER BY started_at DESC
 LIMIT ? OFFSET ?`, limit, offset).Scan(&rows).Error
	if err != nil {
		return nil, 0, translate(err)
	}
	out := make([]BackupRunSummary, 0, len(rows))
	for _, x := range rows {
		out = append(out, BackupRunSummary{
			RunID:         x.RunID,
			ScheduleID:    x.ScheduleID,
			Kind:          x.Kind,
			Content:       x.Content,
			HasAccounts:   x.HasAccounts,
			Accounts:      x.Accounts,
			Total:         x.Total,
			Succeeded:     x.Succeeded,
			Failed:        x.Failed,
			Running:       x.Running,
			Queued:        x.Queued,
			Cancelled:     x.Cancelled,
			Partial:       x.Partial,
			BytesAdded:    x.BytesAdded,
			BytesTotal:    x.BytesTotal,
			StartedAt:     x.StartedAt,
			LatestUpdated: x.LatestUpdated,
		})
	}
	return out, total, nil
}

func (r *backupJobRepo) ListByRun(ctx context.Context, runID string) ([]models.BackupJob, error) {
	var out []models.BackupJob
	err := r.db.WithContext(ctx).
		Where("run_id = ?", runID).
		Order("created_at ASC").
		Find(&out).Error
	if err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func (r *backupJobRepo) ListManual(ctx context.Context, limit, offset int) ([]models.BackupJob, int64, error) {
	return r.list(ctx, "run_id IS NULL", nil, limit, offset)
}

// ListRunning returns jobs currently in `running`, oldest-started first.
//
// The finalizer MUST use this rather than filtering ListAll in memory.
// ListAll pages `created_at DESC`, while the dispatcher admits work via
// ListQueuedOldest (`created_at ASC`) — so the running jobs are the
// OLDEST-created of a fan-out batch and fall outside the newest-N page as
// soon as a schedule creates more than N jobs. Those jobs could then be
// neither finalized nor stall-timed-out: they stayed `running` forever,
// CountByStatus(running) stayed at the cap, and the whole backup queue
// deadlocked until someone edited the DB by hand.
//
// Indexed by idx_backup_jobs_status.
func (r *backupJobRepo) ListRunning(ctx context.Context, limit int) ([]models.BackupJob, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []models.BackupJob
	err := r.db.WithContext(ctx).
		Where("status = ?", models.BackupJobStatusRunning).
		Order("started_at ASC").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, translate(err)
	}
	return out, nil
}

// CountForUser counts a user's jobs without materialising any rows. The
// retention cap check used ListForUser(userID, 1, 0), which runs a COUNT(*)
// AND a LIMIT 1 SELECT purely to read the total — once per (user,
// destination) pair inside the enqueue tick.
func (r *backupJobRepo) CountForUser(ctx context.Context, userID string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&models.BackupJob{}).
		Where("user_id = ?", userID).
		Count(&n).Error
	if err != nil {
		return 0, translate(err)
	}
	return n, nil
}

func (r *backupJobRepo) ListQueuedOldest(ctx context.Context, limit int) ([]models.BackupJob, error) {
	if limit <= 0 {
		limit = 10
	}
	var out []models.BackupJob
	err := r.db.WithContext(ctx).
		Where("status = ?", models.BackupJobStatusQueued).
		Order("created_at ASC").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func (r *backupJobRepo) MarkStarted(ctx context.Context, id string) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).
		Model(&models.BackupJob{}).
		Where("id = ? AND status = ?", id, models.BackupJobStatusQueued).
		Updates(map[string]any{
			"status":     models.BackupJobStatusRunning,
			"started_at": now,
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFinished closes a job. Status must be one of succeeded/partial/
// failed/cancelled; callers are expected to pre-validate. Manifest and
// warnings are pass-through JSON so the worker can serialize whatever
// shape ADR-0075 / Step 2 finalizes.
func (r *backupJobRepo) MarkFinished(
	ctx context.Context,
	id, status string,
	snapshotID, parentSnapshot string,
	bytesAdded, bytesTotal uint64,
	manifest, warnings json.RawMessage,
	errText string,
) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":          status,
		"finished_at":     now,
		"snapshot_id":     snapshotID,
		"parent_snapshot": parentSnapshot,
		"bytes_added":     bytesAdded,
		"bytes_total":     bytesTotal,
	}
	if len(manifest) > 0 {
		updates["manifest_json"] = manifest
	}
	if len(warnings) > 0 {
		updates["warnings_json"] = warnings
	}
	if errText != "" {
		updates["error_text"] = errText
	}
	res := r.db.WithContext(ctx).
		Model(&models.BackupJob{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListByStatusSince implements BackupJobRepository.
func (r *backupJobRepo) ListByStatusSince(ctx context.Context, status string, since time.Time, limit int) ([]models.BackupJob, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []models.BackupJob
	if err := r.db.WithContext(ctx).
		Where("status = ? AND finished_at >= ?", status, since).
		Order("finished_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
