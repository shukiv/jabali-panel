package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeDropCountRepo records every SetDropCount per user.
type fakeDropCountRepo struct {
	repository.UserEgressPolicyRepository
	counts map[string][]uint64
}

func (f *fakeDropCountRepo) SetDropCount(_ context.Context, userID string, n uint64, _ time.Time) error {
	f.counts[userID] = append(f.counts[userID], n)
	return nil
}

func setCounter(a *fakeAgent, packets uint64) {
	a.resultByMethod = map[string]json.RawMessage{
		"user.egress.read_counters": json.RawMessage(fmt.Sprintf(`{"counters":[{"username":"alice","packets":%d}]}`, packets)),
	}
}

// After a panel restart the in-memory baseline is empty while the nft
// counter still holds everything since the table was loaded. The first
// read records the baseline and reports 0, so a restart never turns the
// whole history into one tick (and one false burst alert).
func TestReadUserEgressCounters_FirstReadIsBaseline(t *testing.T) {
	userEgressMu.Lock()
	saved := userEgressLastSeen
	userEgressLastSeen = map[string]uint64{}
	userEgressMu.Unlock()
	t.Cleanup(func() {
		userEgressMu.Lock()
		userEgressLastSeen = saved
		userEgressMu.Unlock()
	})

	agent := &fakeAgent{}
	repo := &fakeDropCountRepo{counts: map[string][]uint64{}}
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, agent, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithUserEgressPolicies(repo)
	ids := map[string]string{"alice": "u1"}

	setCounter(agent, 5000) // accumulated before the restart
	r.readUserEgressCounters(context.Background(), ids)
	setCounter(agent, 5030)
	r.readUserEgressCounters(context.Background(), ids)
	setCounter(agent, 10) // table reloaded: the counter starts again
	r.readUserEgressCounters(context.Background(), ids)

	got := repo.counts["u1"]
	want := []uint64{0, 30, 10}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("drop counts = %v, want %v", got, want)
	}
}
