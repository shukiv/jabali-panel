package diskusagesweeper

// The sweeper exists so users.disk_used_kb is a real column the list query
// can ORDER BY. The behaviours worth pinning are the ones that decide
// whether the sorted column tells the truth: a failed measurement must
// leave the previous snapshot alone rather than writing a zero, and a
// user with no linux account must not be measured at all.

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
	mu             sync.Mutex
	calls          []string
	reply          string
	err            error
	replyFor       map[string]string
	sawMeasureDisk bool
}

func (a *stubAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := ""
	if m, ok := params.(map[string]any); ok {
		name, _ = m["username"].(string)
		if md, _ := m["measure_disk"].(bool); md {
			a.sawMeasureDisk = true
		}
	}
	a.calls = append(a.calls, cmd+":"+name)
	if a.err != nil {
		return nil, a.err
	}
	if r, ok := a.replyFor[name]; ok {
		return json.RawMessage(r), nil
	}
	return json.RawMessage(a.reply), nil
}

func (a *stubAgent) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

type persisted struct {
	used, limit uint64
}

type stubUserRepo struct {
	repository.UserRepository
	rows    []models.User
	listErr error

	mu       sync.Mutex
	written  map[string]persisted
	writeErr error
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

// Speed the paced sweep up for tests — the delay exists to be kind to the
// disk in production, not to make the suite slow.
func newTestSweeper(t *testing.T, repo *stubUserRepo, ag *stubAgent) *Sweeper {
	t.Helper()
	s := New(Deps{Users: repo, Agent: ag, QuotaMount: "/home", Log: quietLogger()})
	if s == nil {
		t.Fatal("New returned nil with all deps supplied")
	}
	return s
}

func TestSweepOnce_PersistsReportedUsage(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{userWith("u1", "alice")}}
	ag := &stubAgent{reply: `{"disk":{"used_kb":1800000,"limit_kb":51200000}}`}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	got, ok := repo.written["u1"]
	if !ok {
		t.Fatal("nothing persisted — the column would stay 0 and sort meaninglessly")
	}
	if got.used != 1800000 || got.limit != 51200000 {
		t.Errorf("persisted %+v, want used=1800000 limit=51200000", got)
	}
}

// GH #1242: the sweep must request the du-fallback so a user with no hosting
// package (hence no quota) still gets a real disk number instead of a blank cell.
func TestSweepOnce_RequestsDuFallback(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{userWith("u1", "alice")}}
	ag := &stubAgent{reply: `{"disk":{"used_kb":4200,"limit_kb":0}}`}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if !ag.sawMeasureDisk {
		t.Fatal("sweep must call user.limits.report with measure_disk=true (GH #1242)")
	}
	// A quota-less user (limit 0) with real bytes still persists its usage.
	if got := repo.written["u1"]; got.used != 4200 {
		t.Fatalf("persisted %+v, want used=4200 for the no-package user", got)
	}
}

// A failed agent call must leave the previous snapshot in place. Writing a
// zero would show every account as empty the moment the agent hiccups, and
// a sort by disk usage would then be actively misleading.
func TestSweepOnce_AgentFailureLeavesSnapshotAlone(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{userWith("u1", "alice")}}
	ag := &stubAgent{err: errors.New("agent socket down")}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if repo.writeCount() != 0 {
		t.Fatalf("a failed measurement was persisted anyway: %+v", repo.written)
	}
}

// Same reasoning: a report that came back without a disk section is not a
// measurement of zero.
func TestSweepOnce_MissingDiskSectionIsNotZero(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{userWith("u1", "alice")}}
	ag := &stubAgent{reply: `{"memory":{"current_bytes":1}}`}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if repo.writeCount() != 0 {
		t.Fatalf("a report with no disk section was persisted as a number: %+v", repo.written)
	}
}

// Admins and not-yet-provisioned rows have no linux account, so there is no
// home and no quota entry to measure. Calling the agent for them wastes a
// fork per sweep and logs a failure every time.
func TestSweepOnce_SkipsUsersWithoutLinuxAccount(t *testing.T) {
	repo := &stubUserRepo{rows: []models.User{
		userWith("admin1", ""),
		userWith("u1", "alice"),
	}}
	ag := &stubAgent{reply: `{"disk":{"used_kb":10,"limit_kb":100}}`}

	newTestSweeper(t, repo, ag).SweepOnce(context.Background())

	if ag.callCount() != 1 {
		t.Fatalf("agent called %d times; the account-less row should be skipped entirely", ag.callCount())
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
		reply: `{"disk":{"used_kb":5,"limit_kb":50}}`,
		replyFor: map[string]string{
			"bob": `{"not":"a disk report"}`,
		},
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

	if ag.callCount() != 0 {
		t.Error("measured users despite failing to list them")
	}
}

// A cancelled context must stop the sweep promptly rather than walking
// every remaining account on a shutting-down panel.
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
