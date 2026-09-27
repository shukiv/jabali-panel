package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeSeedPolicyRepo records the seed call and the order of calls.
type fakeSeedPolicyRepo struct {
	repository.UserEgressPolicyRepository
	calls     []string
	seedState string
	seedAt    time.Time
	seedErr   error
}

func (f *fakeSeedPolicyRepo) SeedMissing(_ context.Context, state string, now time.Time) (int64, error) {
	f.calls = append(f.calls, "seed")
	f.seedState, f.seedAt = state, now
	return 2, f.seedErr
}

func (f *fakeSeedPolicyRepo) ListAllForReconcile(context.Context) ([]repository.PolicyForReconcile, error) {
	f.calls = append(f.calls, "list")
	return nil, nil
}

func seedFixture(t *testing.T, mode string) (*Reconciler, *fakeSeedPolicyRepo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "per-user-egress.mode")
	if mode != "" {
		if err := os.WriteFile(path, []byte(mode+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := egressModePath
	egressModePath = path
	t.Cleanup(func() { egressModePath = old })
	repo := &fakeSeedPolicyRepo{}
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, &fakeAgent{}, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithUserEgressPolicies(repo)
	return r, repo
}

// A user without a policy row is not in the egress payload at all. The
// tick seeds the missing rows before it lists the policies, so a user
// created by any path is behind the firewall from the same tick.
func TestReconcileUserEgress_SeedsBeforeListing(t *testing.T) {
	r, repo := seedFixture(t, "enforced")
	r.reconcileUserEgress(context.Background())
	if len(repo.calls) < 2 || repo.calls[0] != "seed" || repo.calls[1] != "list" {
		t.Fatalf("calls = %v, want seed then list", repo.calls)
	}
}

// The seed state follows the host's mode file: learning on a host that
// predated M34, enforced on a fresh install.
func TestReconcileUserEgress_SeedStateFollowsTheModeFile(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"learning", "learning"},
		{"enforced", "enforced"},
	} {
		r, repo := seedFixture(t, tc.mode)
		before := time.Now()
		r.reconcileUserEgress(context.Background())
		if repo.seedState != tc.want {
			t.Errorf("mode %q: seeded %q, want %q", tc.mode, repo.seedState, tc.want)
		}
		if repo.seedAt.Before(before) {
			t.Errorf("mode %q: seed time %v is not this tick's", tc.mode, repo.seedAt)
		}
	}
}

// A missing or unknown mode file must not leave users out, and must not
// seed them into learning: it fails closed to enforced.
func TestReconcileUserEgress_UnreadableModeSeedsEnforced(t *testing.T) {
	for _, mode := range []string{"", "off", "garbage"} {
		r, repo := seedFixture(t, mode)
		r.reconcileUserEgress(context.Background())
		if repo.seedState != "enforced" {
			t.Errorf("mode %q: seeded %q, want enforced", mode, repo.seedState)
		}
	}
}

// A seed failure is logged; the tick still applies the rows that exist.
func TestReconcileUserEgress_SeedFailureStillApplies(t *testing.T) {
	r, repo := seedFixture(t, "enforced")
	repo.seedErr = errors.New("db down")
	r.reconcileUserEgress(context.Background())
	if len(repo.calls) < 2 || repo.calls[1] != "list" {
		t.Fatalf("calls = %v, want the tick to go on to list", repo.calls)
	}
}
