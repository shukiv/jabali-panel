package reconciler

// Until cron.remove looked where cron.apply writes a tenant's units, deleting
// a job left its timer running. The orphan sweep only visited accounts that
// still had cron rows, so an account whose last job was deleted kept every
// leaked timer. The sweep now visits every account once per panel start.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeCronRepo returns listed from ListAll (the pass's snapshot) while
// FindByID sees byID (the database now).
type fakeCronRepo struct {
	listed []*models.CronJob
	byID   map[string]*models.CronJob
}

func (f *fakeCronRepo) Create(context.Context, *models.CronJob) error { return nil }
func (f *fakeCronRepo) FindByID(_ context.Context, id string) (*models.CronJob, error) {
	if j, ok := f.byID[id]; ok {
		return j, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakeCronRepo) ListByUserID(context.Context, string) ([]*models.CronJob, error) {
	return nil, nil
}
func (f *fakeCronRepo) ListAll(context.Context) ([]*models.CronJob, error) { return f.listed, nil }
func (f *fakeCronRepo) Update(context.Context, *models.CronJob) error      { return nil }
func (f *fakeCronRepo) UpdateStatus(context.Context, string, time.Time, int, string) error {
	return nil
}
func (f *fakeCronRepo) Delete(context.Context, string) error { return nil }

// cronDiskAgent holds each account's cron timers on disk and records what the
// reconciler lists and removes.
type cronDiskAgent struct {
	mu       sync.Mutex
	onDisk   map[string][]string
	listFail map[string]bool
	listed   []string
	removed  []string
}

func (a *cronDiskAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, _ := params.(map[string]any)
	username, _ := p["username"].(string)
	switch method {
	case "cron.list":
		a.listed = append(a.listed, username)
		if a.listFail[username] {
			return nil, fmt.Errorf("agent down")
		}
		return json.Marshal(map[string][]string{"unit_files": a.onDisk[username]})
	case "cron.remove":
		jobID, _ := p["job_id"].(string)
		a.removed = append(a.removed, username+"/"+jobID)
		var kept []string
		for _, id := range a.onDisk[username] {
			if id != jobID {
				kept = append(kept, id)
			}
		}
		a.onDisk[username] = kept
	}
	return nil, nil
}

func (a *cronDiskAgent) listedCount(username string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, u := range a.listed {
		if u == username {
			n++
		}
	}
	return n
}

func (a *cronDiskAgent) removedSorted() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := append([]string(nil), a.removed...)
	sort.Strings(out)
	return out
}

func cronSweepUser(id, username string, admin bool) *models.User {
	u := &models.User{ID: id, Email: id + "@example.com", IsAdmin: admin}
	if username != "" {
		u.Username = &username
	}
	return u
}

func newCronSweepReconciler(ag *cronDiskAgent, cron *fakeCronRepo, users ...*models.User) *Reconciler {
	repo := &fakeUserRepo{users: map[string]*models.User{}}
	for _, u := range users {
		repo.users[u.ID] = u
	}
	return New(&fakeDomainRepo{}, repo, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithCronJobs(cron)
}

func TestCronSweep_RemovesLeakedTimersOfAccountsWithNoJobsLeft(t *testing.T) {
	aliceJob := &models.CronJob{ID: "01JOBALICE0000000000000000", UserID: "u-alice", Enabled: true}
	// erin's job was created after this pass read the cron rows.
	erinJob := &models.CronJob{ID: "01JOBERINNEW00000000000000", UserID: "u-erin", Enabled: true}
	cron := &fakeCronRepo{
		listed: []*models.CronJob{aliceJob},
		byID:   map[string]*models.CronJob{aliceJob.ID: aliceJob, erinJob.ID: erinJob},
	}
	ag := &cronDiskAgent{onDisk: map[string][]string{
		"alice": {aliceJob.ID, "01JOBALICEGONE000000000000"},
		"bob":   {"01JOBBOBGONE10000000000000", "01JOBBOBGONE20000000000000"},
		"carol": {"01JOBCAROLADMIN00000000000"},
		"erin":  {erinJob.ID},
	}}
	r := newCronSweepReconciler(ag, cron,
		cronSweepUser("u-alice", "alice", false),
		cronSweepUser("u-bob", "bob", false),    // every job deleted
		cronSweepUser("u-carol", "carol", true), // an admin
		cronSweepUser("u-dave", "", false),      // no Linux account
		cronSweepUser("u-erin", "erin", false))

	r.reconcileCronJobs(context.Background())

	want := []string{"alice/01JOBALICEGONE000000000000", "bob/01JOBBOBGONE10000000000000", "bob/01JOBBOBGONE20000000000000"}
	if got := ag.removedSorted(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("removed %v, want %v", got, want)
	}
	if ag.listedCount("carol") != 0 || ag.listedCount("") != 0 {
		t.Errorf("listed an admin or an account without a Linux username: %v", ag.listed)
	}

	// Done for this process: the next tick does not visit bob again.
	r.reconcileCronJobs(context.Background())
	if n := ag.listedCount("bob"); n != 1 {
		t.Errorf("bob listed %d times over two ticks, want once", n)
	}
}

func TestCronSweep_RetriesUntilAPassSucceeds(t *testing.T) {
	cron := &fakeCronRepo{byID: map[string]*models.CronJob{}}
	ag := &cronDiskAgent{
		onDisk:   map[string][]string{"bob": {"01JOBBOBGONE10000000000000"}},
		listFail: map[string]bool{"bob": true},
	}
	r := newCronSweepReconciler(ag, cron, cronSweepUser("u-bob", "bob", false))

	r.reconcileCronJobs(context.Background())
	if len(ag.removedSorted()) != 0 {
		t.Fatalf("removed %v while cron.list failed", ag.removedSorted())
	}

	ag.listFail["bob"] = false
	r.reconcileCronJobs(context.Background())
	if got := ag.removedSorted(); fmt.Sprint(got) != "[bob/01JOBBOBGONE10000000000000]" {
		t.Fatalf("after the agent came back, removed %v", got)
	}

	r.reconcileCronJobs(context.Background())
	if n := ag.listedCount("bob"); n != 2 {
		t.Errorf("bob listed %d times, want twice (the failed pass, then the clean one)", n)
	}
}
