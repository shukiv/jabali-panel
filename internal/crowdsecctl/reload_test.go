package crowdsecctl

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHost struct {
	mu       sync.Mutex
	cmds     []string
	upAfter  int // Up answers from this check on; <0 = never until restarted
	checks   int
	restarts int
	failRun  bool
}

func (h *fakeHost) recovery() Recovery {
	return Recovery{
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			cmd := name + " " + strings.Join(args, " ")
			h.cmds = append(h.cmds, cmd)
			if cmd == "systemctl restart crowdsec" {
				if h.failRun {
					return []byte("Job failed"), errors.New("exit 1")
				}
				h.restarts++
			}
			return nil, nil
		},
		Up: func(context.Context) bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.checks++
			if h.restarts > 0 {
				return true
			}
			return h.upAfter >= 0 && h.checks > h.upAfter
		},
		Wait: 30 * time.Millisecond,
		Poll: time.Millisecond,
	}
}

// A normal reload: AppSec is back after a few checks, nothing is restarted.
func TestAfterReload_BackInTime(t *testing.T) {
	h := &fakeHost{upAfter: 3}
	restarted, err := h.recovery().AfterReload(context.Background())
	if err != nil || restarted || len(h.cmds) != 0 {
		t.Fatalf("restarted=%v err=%v cmds=%v", restarted, err, h.cmds)
	}
}

// The production hang: AppSec never comes back. crowdsec is killed (its stop
// would hang) and restarted, and AppSec answers again.
func TestAfterReload_HungIsKilledAndRestarted(t *testing.T) {
	h := &fakeHost{upAfter: -1}
	restarted, err := h.recovery().AfterReload(context.Background())
	if err != nil || !restarted {
		t.Fatalf("restarted=%v err=%v", restarted, err)
	}
	want := []string{"systemctl kill --signal=SIGKILL crowdsec", "systemctl restart crowdsec"}
	if strings.Join(h.cmds, "|") != strings.Join(want, "|") {
		t.Fatalf("cmds %v, want %v", h.cmds, want)
	}
}

func TestAfterReload_RestartFailureIsReported(t *testing.T) {
	h := &fakeHost{upAfter: -1, failRun: true}
	restarted, err := h.recovery().AfterReload(context.Background())
	if err == nil || !restarted || !strings.Contains(err.Error(), "restart failed") {
		t.Fatalf("restarted=%v err=%v", restarted, err)
	}
}

// A caller that gives up stops the wait; nothing is killed on its behalf.
func TestAfterReload_CancelledContextKillsNothing(t *testing.T) {
	h := &fakeHost{upAfter: -1}
	r := h.recovery()
	r.Wait = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := r.AfterReload(ctx); err == nil || len(h.cmds) != 0 {
		t.Fatalf("err=%v cmds=%v", err, h.cmds)
	}
}

// A second reload just before the deadline starts the wait over: back-to-back
// reloads are slow, not hung, and nothing is killed. AppSec answers 20 polls
// (at least 20ms) after that reload, which fits only in a fresh 100ms wait,
// never in the 10ms left of the first.
func TestAfterReload_AnotherReloadRestartsTheWait(t *testing.T) {
	var mu sync.Mutex
	var cmds []string
	start := time.Now()
	fired, after := false, 0
	r := Recovery{
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			cmds = append(cmds, name+" "+strings.Join(args, " "))
			return nil, nil
		},
		Up: func(context.Context) bool {
			if fired {
				after++
			}
			return after > 20
		},
		Wait: 100 * time.Millisecond,
		Poll: time.Millisecond,
		Again: func() bool {
			if !fired && time.Since(start) >= 90*time.Millisecond {
				fired = true
				return true
			}
			return false
		},
	}
	restarted, err := r.AfterReload(context.Background())
	if err != nil || restarted || len(cmds) != 0 || !fired {
		t.Fatalf("restarted=%v err=%v cmds=%v fired=%v", restarted, err, cmds, fired)
	}
}
