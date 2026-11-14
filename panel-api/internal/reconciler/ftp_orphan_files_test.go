package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// orphanAgent answers the two verbs of the deleted-account file pass.
// orphan_uids replies come from orphans in order (the last one repeats).
type orphanAgent struct {
	mu        sync.Mutex
	orphans   [][]ftpOrphanUID
	queryErr  error
	reown     map[string]ftpReownResult
	queries   int
	walked    []string
	keepSeen  [][]uint32
	mountSeen string
}

func (a *orphanAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := params.(map[string]any)
	switch method {
	case "ftpaccount.orphan_uids":
		if a.queryErr != nil {
			return nil, a.queryErr
		}
		a.mountSeen = p["mount"].(string)
		i := min(a.queries, len(a.orphans)-1)
		a.queries++
		return json.Marshal(map[string]any{"orphans": a.orphans[i]})
	case "ftpaccount.reown_orphans":
		t := p["tenant_username"].(string)
		a.walked = append(a.walked, t)
		a.keepSeen = append(a.keepSeen, p["keep_uids"].([]uint32))
		return json.Marshal(a.reown[t])
	}
	return nil, errors.New("unexpected " + method)
}

// pagedUsers lists users by username in pages, as the repository does.
type pagedUsers struct {
	repository.UserRepository
	names []string
	pages int
}

func (u *pagedUsers) List(_ context.Context, opts repository.ListOptions) ([]models.User, int64, error) {
	u.pages++
	names := append([]string(nil), u.names...)
	sort.Strings(names)
	var out []models.User
	for i := opts.Offset; i < len(names) && len(out) < opts.Limit; i++ {
		n := names[i]
		out = append(out, models.User{ID: "id-" + n, Username: &n})
	}
	out = append(out, models.User{ID: "admin"}) // no system account
	return out[:min(len(out), opts.Limit)], int64(len(names)), nil
}

func orphanReconciler(a *orphanAgent, users ...string) *Reconciler {
	r := New(nil, &pagedUsers{names: users}, a, slog.Default(), Config{})
	r.WithFtpOrphanSweep(func() (string, error) { return "/home", nil })
	return r
}

func TestFtpOrphanSweep_NothingLeftWalksNothing(t *testing.T) {
	a := &orphanAgent{orphans: [][]ftpOrphanUID{{}}}
	r := orphanReconciler(a, "shop")
	r.sweepFtpOrphanFiles(context.Background(), []uint32{}, nil)
	if len(a.walked) != 0 || a.queries != 1 || a.mountSeen != "/home" {
		t.Errorf("walked %v after %d queries of %q, want one query and no walk", a.walked, a.queries, a.mountSeen)
	}
}

func TestFtpOrphanSweep_WalksTenantsWithAccountsFirstUntilAllAreFound(t *testing.T) {
	a := &orphanAgent{
		orphans: [][]ftpOrphanUID{{{UID: 1000000005, Files: 3, UsedKB: 10}}, {}},
		reown: map[string]ftpReownResult{
			"shop":  {Reowned: map[string]int{"1000000005": 1}},
			"alice": {Reowned: map[string]int{"1000000005": 2}},
		},
	}
	r := orphanReconciler(a, "alice", "shop", "zed")
	keep := []uint32{1000000009}
	r.sweepFtpOrphanFiles(context.Background(), keep, []string{"shop"})

	if want := []string{"shop", "alice"}; len(a.walked) != 2 || a.walked[0] != want[0] || a.walked[1] != want[1] {
		t.Errorf("walked %v, want %v and then stop", a.walked, want)
	}
	for _, k := range a.keepSeen {
		if len(k) != 1 || k[0] != 1000000009 {
			t.Errorf("keep_uids = %v, want the rows' uids", k)
		}
	}
}

func TestFtpOrphanSweep_LeftoversNotFoundWaitADay(t *testing.T) {
	left := []ftpOrphanUID{{UID: 1000000005, Files: 3}}
	a := &orphanAgent{orphans: [][]ftpOrphanUID{left}, reown: map[string]ftpReownResult{}}
	r := orphanReconciler(a, "alice", "shop")

	r.sweepFtpOrphanFiles(context.Background(), []uint32{}, nil)
	if len(a.walked) != 2 {
		t.Fatalf("first pass walked %v, want both tenants", a.walked)
	}
	r.sweepFtpOrphanFiles(context.Background(), []uint32{}, nil)
	if len(a.walked) != 2 {
		t.Errorf("the same leftovers were looked for again: %v", a.walked)
	}
	r.ftpOrphanAt = time.Now().Add(-ftpOrphanRetryAfter - time.Minute)
	r.sweepFtpOrphanFiles(context.Background(), []uint32{}, nil)
	if len(a.walked) != 4 {
		t.Errorf("not looked for again after a day: %v", a.walked)
	}
}

func TestFtpOrphanSweep_IncompleteRoundIsRetriedNextPass(t *testing.T) {
	for name, res := range map[string]ftpReownResult{
		"stopped": {Stopped: true},
		"failed":  {Failed: 1, FirstError: "chown x: disk quota exceeded"},
	} {
		t.Run(name, func(t *testing.T) {
			a := &orphanAgent{
				orphans: [][]ftpOrphanUID{{{UID: 1000000005, Files: 3}}},
				reown:   map[string]ftpReownResult{"shop": res},
			}
			r := orphanReconciler(a, "shop")
			r.sweepFtpOrphanFiles(context.Background(), []uint32{}, nil)
			r.sweepFtpOrphanFiles(context.Background(), []uint32{}, nil)
			if len(a.walked) != 2 {
				t.Errorf("walked %v, want the incomplete round retried", a.walked)
			}
		})
	}
}

func TestFtpOrphanSweep_OffWithoutTheVerbOrTheMount(t *testing.T) {
	a := &orphanAgent{queryErr: errors.New("unknown command ftpaccount.orphan_uids")}
	r := orphanReconciler(a, "shop")
	r.sweepFtpOrphanFiles(context.Background(), []uint32{}, nil)
	if len(a.walked) != 0 {
		t.Errorf("walked %v with an agent that has no verb", a.walked)
	}

	b := &orphanAgent{orphans: [][]ftpOrphanUID{{{UID: 1000000005, Files: 3}}}}
	r = orphanReconciler(b, "shop")
	r.WithFtpOrphanSweep(func() (string, error) { return "", errors.New("no quota") })
	r.sweepFtpOrphanFiles(context.Background(), []uint32{}, nil)
	if b.queries != 0 {
		t.Error("queried without a quota mount")
	}
}

func TestFtpOrphanInputs(t *testing.T) {
	u1, u2 := uint32(1000000001), uint32(1000000002)
	rows := []models.FtpAccount{
		{UserID: "u2", Isolated: true, UID: &u2},
		{UserID: "u1", Isolated: true, UID: &u1},
		{UserID: "u1", Isolated: true, UID: &u1},
		{UserID: "u3"}, // a same-uid alias
	}
	keep, first := ftpOrphanInputs(rows, map[string]string{"u1": "shop", "u2": "alice", "u3": "zed"})
	if len(keep) != 3 {
		t.Errorf("keep = %v, want every row's uid", keep)
	}
	if len(first) != 2 || first[0] != "alice" || first[1] != "shop" {
		t.Errorf("first = %v, want the tenants with isolated rows", first)
	}
}

func TestKickFtpOrphanSweep_OneAtATimeAndOnlyWhenOn(t *testing.T) {
	a := &orphanAgent{orphans: [][]ftpOrphanUID{{}}}
	r := New(nil, &pagedUsers{}, a, slog.Default(), Config{})
	r.kickFtpOrphanSweep(context.Background(), nil, nil)
	r.WithFtpOrphanSweep(func() (string, error) { return "/home", nil })
	r.ftpOrphanRunning.Store(true)
	r.kickFtpOrphanSweep(context.Background(), nil, nil)
	time.Sleep(20 * time.Millisecond)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.queries != 0 {
		t.Error("a pass started while off or while one was running")
	}
}

// The FTP pass starts the deleted-account file pass.
func TestReconcileFtpAccounts_StartsTheDeletedAccountFilePass(t *testing.T) {
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"ftpaccount.list":        hostListResult(t, nil),
		"ftpaccount.list_all":    hostListAllResult(t, nil),
		"ftpaccount.orphan_uids": json.RawMessage(`{"orphans":[]}`),
	}}
	r := ftpTestReconciler(t, agent, nil, nil)
	r.WithFtpOrphanSweep(func() (string, error) { return "/home", nil })

	r.reconcileFtpAccounts(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for r.ftpOrphanRunning.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	for _, c := range agent.calls {
		if c.method == "ftpaccount.orphan_uids" {
			return
		}
	}
	t.Error("ftpaccount.orphan_uids was never called")
}

// The panel and the agent agree on the verbs and their JSON keys.
func TestFtpOrphanAgentContract(t *testing.T) {
	src, err := os.ReadFile("../../../panel-agent/internal/commands/ftp_account_reown.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"ftpaccount.orphan_uids", "ftpaccount.reown_orphans"} {
		if !regexp.MustCompile(`Register\("` + regexp.QuoteMeta(verb) + `"`).Match(src) {
			t.Errorf("agent does not register %s", verb)
		}
	}
	// Keys the panel sends or reads: a struct tag or a map key in the agent.
	for _, k := range []string{"mount", "orphans", "uid", "files", "used_kb", "tenant_username", "keep_uids", "reowned", "scanned", "stopped", "failed", "first_error", "linked"} {
		if !regexp.MustCompile(`json:"` + k + `[",]|"` + k + `":`).Match(src) {
			t.Errorf("agent has no %q key", k)
		}
	}
}
