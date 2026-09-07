package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ulFakeAgent returns a canned user.limits.report payload so a test can assert
// the handler passes `current` through untouched independently of disk_used.
type ulFakeAgent struct{ raw string }

func (a ulFakeAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	return json.RawMessage(a.raw), nil
}

// GH #1439: the tenant dashboard's Disk metric used to read the POSIX quota in
// `current`, which the agent omits when quota isn't tracking — the card then
// showed 0 B. The usage endpoint now surfaces the persisted Disk Usage
// snapshot's home `du` figure as `disk_used` so the dashboard matches the Disk
// Usage page. These tests pin that contract.

type ulSnapshotRepo struct {
	repository.DiskUsageSnapshotRepository
	snap *models.DiskUsageSnapshot
}

func (r *ulSnapshotRepo) Get(context.Context, string) (*models.DiskUsageSnapshot, error) {
	if r.snap == nil {
		return nil, repository.ErrNotFound
	}
	return r.snap, nil
}

func snapshotPayload(t *testing.T, filesBytes, emailBytes, dbBytes uint64) string {
	t.Helper()
	b, err := json.Marshal(diskUsageResponse{
		TotalBytes: filesBytes + emailBytes + dbBytes,
		Files:      diskUsageCategory{Bytes: filesBytes},
		Email:      diskUsageCategory{Bytes: emailBytes},
		Databases:  diskUsageCategory{Bytes: dbBytes},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(b)
}

func baseUsageCfg(snap repository.DiskUsageSnapshotRepository) UserLimitsHandlerConfig {
	uname := "alice"
	return UserLimitsHandlerConfig{
		Users:          &ulUserRepo{u: &models.User{ID: "u1", Username: &uname}},
		Packages:       &ulPackageRepo{},
		LimitOverrides: &ulOverrideRepo{},
		// No Agent: reproduces the reported case (quota absent → `current` nil).
		DiskSnapshots: snap,
	}
}

// With a snapshot present and NO agent (the exact 0-B scenario), the endpoint
// reports disk_used from the snapshot's Files bytes — not email/db, so it stays
// apples-to-apples with the home quota the card renders — and leaves `current`
// nil. This is the whole point: the disk number no longer depends on quota.
func TestUsage_DiskUsedFromSnapshot(t *testing.T) {
	const files = uint64(5 * 1024 * 1024 * 1024) // 5 GiB home
	computed := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	cfg := baseUsageCfg(&ulSnapshotRepo{snap: &models.DiskUsageSnapshot{
		UserID:     "u1",
		Payload:    snapshotPayload(t, files, 900*1024*1024, 300*1024*1024),
		ComputedAt: computed,
	}})

	resp := serveUsage(t, cfg)

	if resp.DiskUsed == nil {
		t.Fatal("disk_used must be present when a snapshot exists")
	}
	if resp.DiskUsed.Bytes != files {
		t.Errorf("disk_used.bytes: want home files %d (not total), got %d", files, resp.DiskUsed.Bytes)
	}
	if resp.DiskUsed.Source != "du" {
		t.Errorf("disk_used.source: want %q, got %q", "du", resp.DiskUsed.Source)
	}
	if resp.DiskUsed.ComputedAt == nil || !resp.DiskUsed.ComputedAt.Equal(computed) {
		t.Errorf("disk_used.computed_at: want %v, got %v", computed, resp.DiskUsed.ComputedAt)
	}
	if resp.Current != nil {
		t.Errorf("no agent → current must stay nil, got %s", string(resp.Current))
	}
}

// Agent AND snapshot both present: `current` passes through unchanged (the
// agent's quota disk figure is preserved for the admin views that read it),
// while disk_used independently carries the snapshot's home `du`. The two
// sources coexist; disk_used never mutates current.
func TestUsage_AgentAndSnapshotCoexist(t *testing.T) {
	uname := "agentsnapuser" // unique key so the 60s usage cache can't cross-contaminate
	cfg := UserLimitsHandlerConfig{
		Users:          &ulUserRepo{u: &models.User{ID: "u1", Username: &uname}},
		Packages:       &ulPackageRepo{},
		LimitOverrides: &ulOverrideRepo{},
		Agent:          ulFakeAgent{raw: `{"username":"agentsnapuser","disk":{"used_kb":7,"limit_kb":0}}`},
		DiskSnapshots:  &ulSnapshotRepo{snap: &models.DiskUsageSnapshot{UserID: "u1", Payload: snapshotPayload(t, 999, 0, 0), ComputedAt: time.Now()}},
	}

	resp := serveUsage(t, cfg)

	if resp.Current == nil {
		t.Fatal("current must pass through when the agent answered")
	}
	var cur struct {
		Disk struct {
			UsedKB uint64 `json:"used_kb"`
		} `json:"disk"`
	}
	if err := json.Unmarshal(resp.Current, &cur); err != nil {
		t.Fatalf("decode current: %v", err)
	}
	if cur.Disk.UsedKB != 7 {
		t.Errorf("current.disk.used_kb must be the agent's 7 (untouched), got %d", cur.Disk.UsedKB)
	}
	if resp.DiskUsed == nil || resp.DiskUsed.Bytes != 999 {
		t.Fatalf("disk_used must carry the snapshot's 999 bytes independently, got %+v", resp.DiskUsed)
	}
}

// No snapshot yet (tenant never opened the Disk Usage page): disk_used is
// omitted and the client falls back to the quota figure — today's behavior.
func TestUsage_NoSnapshotOmitsDiskUsed(t *testing.T) {
	resp := serveUsage(t, baseUsageCfg(&ulSnapshotRepo{snap: nil}))
	if resp.DiskUsed != nil {
		t.Errorf("no snapshot → disk_used must be nil, got %+v", resp.DiskUsed)
	}
}

// A nil DiskSnapshots repo (a deployment that hasn't wired it) leaves the
// response exactly as before — disk_used absent.
func TestUsage_NilSnapshotRepoIsBackwardCompatible(t *testing.T) {
	resp := serveUsage(t, baseUsageCfg(nil))
	if resp.DiskUsed != nil {
		t.Errorf("nil snapshot repo → disk_used must be nil, got %+v", resp.DiskUsed)
	}
}
