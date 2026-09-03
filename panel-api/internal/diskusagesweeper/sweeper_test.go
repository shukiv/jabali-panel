package diskusagesweeper

// The sweeper keeps users.disk_used_kb a real column the list query can ORDER
// BY. Behaviours worth pinning: a failed measurement leaves the previous
// snapshot alone (never a zero), a user with no linux account is not measured,
// and — since JAB-376 — one host quota inventory replaces the per-user quota
// fan-out while the du-fallback for empty/quota-0 homes (GH #1242) survives.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type stubAgent struct {
	mu sync.Mutex
	// user.limits.report (the per-user du path)
	reportCalls    []string
	reply          string
	replyFor       map[string]string
	sawMeasureDisk bool
	// system.quota_inventory (the one-shot batch path)
	inventoryCalls int
	inventoryReply string // "" => empty inventory
	// err fails every call (whole agent down)
	err error
}

func (a *stubAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cmd == "system.quota_inventory" {
		a.inventoryCalls++
		if a.err != nil {
			return nil, a.err
		}
		if a.inventoryReply == "" {
			return json.RawMessage(`{"entries":[],"partial":false}`), nil
		}
		return json.RawMessage(a.inventoryReply), nil
	}
	// user.limits.report
	name := ""
	if m, ok := params.(map[string]any); ok {
		name, _ = m["username"].(string)
		if md, _ := m["measure_disk"].(bool); md {
			a.sawMeasureDisk = true
		}
	}
	a.reportCalls = append(a.reportCalls, name)
	if a.err != nil {
		return nil, a.err
	}
	if r, ok := a.replyFor[name]; ok {
		return json.RawMessage(r), nil
	}
	return json.RawMessage(a.reply), nil
}

func (a *stubAgent) reportCallCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.reportCalls)
}
func (a *stubAgent) inventoryCallCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inventoryCalls
}

type persisted struct {
	used, limit uint64
}

type stubUserRepo struct {
	repository.UserRepository
	rows    []models.User
	listErr error

	mu         sync.Mutex
	written    map[string]persisted
	batchCalls int
	writeErr   error
}

func (r *stubUserRepo) List(_ context.Context, _ repository.ListOptions) ([]models.User, int64, error) {
	if r.listErr != nil {
		return nil, 0, r.listErr
	}
	return r.rows, int64(len(r.rows)), nil
}

func (r *stubUserRepo) UpdateDiskUsage(_ context.Context, id string, used, limit uint64, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return r.writeErr
	}
	if r.written == nil {
		r.written = map[string]persisted{}
	}
	r.written[id] = persisted{used: used, limit: limit}
	return nil
}

// BatchUpdateDiskUsage records into the SAME map so the persist assertions read
// the batch, and counts the calls so a test can prove the whole sweep is one
// bounded persist, not one statement per account.
func (r *stubUserRepo) BatchUpdateDiskUsage(_ context.Context, rows []repository.DiskUsageRow, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return r.writeErr
	}
	r.batchCalls++
	if r.written == nil {
		r.written = map[string]persisted{}
	}
	for _, row := range rows {
		r.written[row.UserID] = persisted{used: row.UsedKB, limit: row.LimitKB}
	}
	return nil
}

func (r *stubUserRepo) writeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.written)
}

func userWith(id, username string) models.User {
	u := models.User{ID: id}
	if username != "" {
		un := username
		u.Username = &un
	}
	return u
}

func newTestSweeper(t *testing.T, repo *stubUserRepo, ag *stubAgent) *Sweeper {
	t.Helper()
	s := New(Deps{Users: repo, Agent: ag, QuotaMount: "/", Log: quietLogger()})
	if s == nil {
		t.Fatal("New returned nil with all deps supplied")
	}
	return s
}

// JAB-376 fast path: a user whose quota the inventory already measured is
// persisted WITHOUT a per-user agent call.
func TestSweepOnce_InventoryFastPathSkipsPerUserCall(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{userWith("u1", "alice")}}
	ag := &stubAgent{inventoryReply: `{"entries":[{"username":"alice","used_kb":999,"limit_kb":5000}]}`}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if ag.reportCallCount() != 0 {
		t.Errorf("inventory covered alice — no per-user user.limits.report should be made, got %d", ag.reportCallCount())
	}
	if ag.inventoryCallCount() != 1 {
		t.Errorf("expected exactly one inventory read, got %d", ag.inventoryCallCount())
	}
	if got := repo.written["u1"]; got.used != 999 || got.limit != 5000 {
		t.Errorf("persisted %+v, want used=999 limit=5000 from the inventory", got)
	}
	if repo.batchCalls != 1 {
		t.Errorf("expected one batch persist, got %d", repo.batchCalls)
	}
}

// A packaged-but-empty account (real hard limit, 0 used) is authoritative from
// the inventory — no du walk needed, even though used is 0.
func TestSweepOnce_InventoryLimitOnlyIsAuthoritative(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{userWith("u1", "newbie")}}
	ag := &stubAgent{inventoryReply: `{"entries":[{"username":"newbie","used_kb":0,"limit_kb":5000000}]}`}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if ag.reportCallCount() != 0 {
		t.Errorf("a limit-set 0-used account is authoritative — no du fallback, got %d calls", ag.reportCallCount())
	}
	if got := repo.written["u1"]; got.used != 0 || got.limit != 5000000 {
		t.Errorf("persisted %+v, want used=0 limit=5000000 from the inventory", got)
	}
}

// A single inventory read + one batch persist covers N accounts.
func TestSweepOnce_OneInventoryAndOneBatchForManyUsers(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{
		userWith("u1", "alice"), userWith("u2", "bob"), userWith("u3", "carol"),
	}}
	ag := &stubAgent{inventoryReply: `{"entries":[
		{"username":"alice","used_kb":10,"limit_kb":100},
		{"username":"bob","used_kb":20,"limit_kb":200},
		{"username":"carol","used_kb":30,"limit_kb":300}]}`}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if ag.inventoryCallCount() != 1 || ag.reportCallCount() != 0 {
		t.Errorf("want 1 inventory + 0 per-user calls, got inv=%d report=%d", ag.inventoryCallCount(), ag.reportCallCount())
	}
	if repo.batchCalls != 1 || repo.writeCount() != 3 {
		t.Errorf("want one batch persisting 3 rows, got batchCalls=%d rows=%d", repo.batchCalls, repo.writeCount())
	}
}

// GH #1242 preserved: an account the inventory reports as 0 (empty / pre-quotaon
// files) falls back to the per-user du walk so its cell isn't a false 0.
func TestSweepOnce_InventoryZeroFallsToDuFallback(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{userWith("u1", "alice")}}
	ag := &stubAgent{
		inventoryReply: `{"entries":[{"username":"alice","used_kb":0,"limit_kb":0}]}`,
		reply:          `{"disk":{"used_kb":4200,"limit_kb":0}}`,
	}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if !ag.sawMeasureDisk {
		t.Fatal("a quota-0 account must fall back to du (measure_disk=true, GH #1242)")
	}
	if got := repo.written["u1"]; got.used != 4200 {
		t.Fatalf("persisted %+v, want du value 4200 for the quota-0 account", got)
	}
}

// The whole sweep persists via one bounded batch, exercising the du path for a
// user absent from the inventory.
func TestSweepOnce_PersistsReportedUsage(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{userWith("u1", "alice")}}
	ag := &stubAgent{reply: `{"disk":{"used_kb":1800000,"limit_kb":51200000}}`} // empty inventory → du path

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	got, ok := repo.written["u1"]
	if !ok {
		t.Fatal("nothing persisted — the column would stay 0 and sort meaninglessly")
	}
	if got.used != 1800000 || got.limit != 51200000 {
		t.Errorf("persisted %+v, want used=1800000 limit=51200000", got)
	}
}

// A whole-agent failure (inventory AND du both down) must leave the previous
// snapshot alone — never a fleet-wide zero the moment the agent hiccups.
func TestSweepOnce_AgentFailureLeavesSnapshotAlone(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{userWith("u1", "alice")}}
	ag := &stubAgent{err: errors.New("agent socket down")}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if repo.writeCount() != 0 {
		t.Fatalf("a failed measurement was persisted anyway: %+v", repo.written)
	}
}

// A du-fallback report with no disk section is not a measurement of zero.
func TestSweepOnce_MissingDiskSectionIsNotZero(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{userWith("u1", "alice")}}
	ag := &stubAgent{reply: `{"memory":{"current_bytes":1}}`} // empty inventory → du → no disk section

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if repo.writeCount() != 0 {
		t.Fatalf("a report with no disk section was persisted as a number: %+v", repo.written)
	}
}

// Admins / not-yet-provisioned rows have no linux account — not measured, and
// the inventory (one call) covers the whole box regardless.
func TestSweepOnce_SkipsUsersWithoutLinuxAccount(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{
		userWith("admin1", ""),
		userWith("u1", "alice"),
	}}
	ag := &stubAgent{reply: `{"disk":{"used_kb":10,"limit_kb":100}}`} // empty inventory → alice du path

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if ag.reportCallCount() != 1 {
		t.Fatalf("per-user reports = %d; the account-less row should be skipped entirely", ag.reportCallCount())
	}
	if _, ok := repo.written["admin1"]; ok {
		t.Error("persisted a snapshot for a user with no linux account")
	}
}

// One bad account must not cost the others their refresh.
func TestSweepOnce_OneUserFailingDoesNotStopTheSweep(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{
		userWith("u1", "alice"),
		userWith("u2", "bob"),
		userWith("u3", "carol"),
	}}
	ag := &stubAgent{
		reply:    `{"disk":{"used_kb":5,"limit_kb":50}}`,
		replyFor: map[string]string{"bob": `{"not":"a disk report"}`},
	}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if repo.writeCount() != 2 {
		t.Fatalf("wrote %d snapshots, want 2 (alice + carol; bob's report was unusable)", repo.writeCount())
	}
	if _, ok := repo.written["u3"]; !ok {
		t.Error("the sweep stopped early — carol never got measured")
	}
}

func TestSweepOnce_ListFailureIsNotFatal(t *testing.T) {
	repo := &stubUserRepo{listErr: errors.New("db down")}
	ag := &stubAgent{reply: `{"disk":{"used_kb":1,"limit_kb":2}}`}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if ag.inventoryCallCount() != 0 || ag.reportCallCount() != 0 {
		t.Error("measured users despite failing to list them")
	}
}

// A cancelled context stops the sweep promptly.
func TestSweepOnce_HonoursContextCancellation(t *testing.T) {
	rows := make([]models.User, 0, 50)
	for i := 0; i < 50; i++ {
		rows = append(rows, userWith(string(rune('a'+i%26))+string(rune('0'+i/26)), "user"+string(rune('a'+i%26))))
	}
	repo := &stubUserRepo{rows: rows}
	ag := &stubAgent{reply: `{"disk":{"used_kb":1,"limit_kb":2}}`}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	newTestSweeper(t, repo, ag).SweepOnce(ctx)

	if repo.writeCount() > 1 {
		t.Errorf("kept sweeping after cancellation: wrote %d", repo.writeCount())
	}
}

func TestNew_NilDepsReturnsNil(t *testing.T) {
	if New(Deps{Agent: &stubAgent{}, Log: quietLogger()}) != nil {
		t.Error("a nil user repo must not yield a running sweeper")
	}
	if New(Deps{Users: &stubUserRepo{}, Log: quietLogger()}) != nil {
		t.Error("a nil agent means nothing to measure with — must not start")
	}
}
