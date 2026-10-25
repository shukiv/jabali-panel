package sendasops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakeLister struct{ pairs []models.SendAsPair }

func (f *fakeLister) ListAllPairs(context.Context) ([]models.SendAsPair, error) {
	return append([]models.SendAsPair(nil), f.pairs...), nil
}

type fakeAgent struct {
	pushed [][]models.SendAsPair
	err    error
}

func (a *fakeAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd != "mail.sendas.reconcile" {
		return nil, errors.New("unexpected command " + cmd)
	}
	a.pushed = append(a.pushed, params.(map[string]any)["pairs"].([]models.SendAsPair))
	if a.err != nil {
		return nil, a.err
	}
	return json.RawMessage(`{"applied":true}`), nil
}

func pair(d, g string) models.SendAsPair {
	return models.SendAsPair{DelegateEmail: d, GrantorEmail: g}
}

func TestSyncIfChanged_SameSetPushesOnce(t *testing.T) {
	ls := &fakeLister{pairs: []models.SendAsPair{pair("info@x.test", "billing@x.test")}}
	ag := &fakeAgent{}
	s := New(ls, ag)
	for i := 0; i < 3; i++ {
		if _, err := s.SyncIfChanged(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if len(ag.pushed) != 1 {
		t.Fatalf("pushes = %d, want 1", len(ag.pushed))
	}
}

// A mailbox enabled, disabled or deleted, or a domain renamed, changes the set
// without a grant change. The next check must push it.
func TestSyncIfChanged_SetChangedWithoutAGrantChangePushes(t *testing.T) {
	ls := &fakeLister{pairs: []models.SendAsPair{pair("info@x.test", "billing@x.test")}}
	ag := &fakeAgent{}
	s := New(ls, ag)
	_, _ = s.SyncIfChanged(context.Background())
	ls.pairs = nil // the grantor mailbox was disabled
	pushed, err := s.SyncIfChanged(context.Background())
	if err != nil || !pushed {
		t.Fatalf("pushed=%v err=%v, want a push", pushed, err)
	}
	if got := ag.pushed[len(ag.pushed)-1]; len(got) != 0 {
		t.Fatalf("last push = %v, want the empty set", got)
	}
}

// After a failed push the same set goes again: the agent may have stored the
// expression and failed the reload, so Stalwart still enforces the old one.
func TestSyncIfChanged_RetriesTheSameSetAfterAFailure(t *testing.T) {
	ls := &fakeLister{pairs: []models.SendAsPair{pair("info@x.test", "billing@x.test")}}
	ag := &fakeAgent{}
	s := New(ls, ag)
	_, _ = s.SyncIfChanged(context.Background())
	ag.err = errors.New("stalwart-cli create: reload failed")
	if err := s.Push(context.Background()); err == nil {
		t.Fatal("Push: want the agent error")
	}
	ag.err = nil
	pushed, err := s.SyncIfChanged(context.Background())
	if err != nil || !pushed {
		t.Fatalf("pushed=%v err=%v, want a retry of the unchanged set", pushed, err)
	}
	if len(ag.pushed) != 3 {
		t.Fatalf("pushes = %d, want 3", len(ag.pushed))
	}
}

// No grants on a fresh install or a restored DB still pushes once, so a
// leftover rule in Stalwart goes back to "true".
func TestSyncIfChanged_FirstCheckPushesTheEmptySet(t *testing.T) {
	ag := &fakeAgent{}
	s := New(&fakeLister{}, ag)
	pushed, err := s.SyncIfChanged(context.Background())
	if err != nil || !pushed {
		t.Fatalf("pushed=%v err=%v, want a push", pushed, err)
	}
	if len(ag.pushed) != 1 || len(ag.pushed[0]) != 0 {
		t.Fatalf("pushed = %v, want one empty set", ag.pushed)
	}
}

func TestPush_PushesAnUnchangedSet(t *testing.T) {
	ls := &fakeLister{pairs: []models.SendAsPair{pair("info@x.test", "billing@x.test")}}
	ag := &fakeAgent{}
	s := New(ls, ag)
	_ = s.Push(context.Background())
	_ = s.Push(context.Background())
	if len(ag.pushed) != 2 {
		t.Fatalf("pushes = %d, want 2", len(ag.pushed))
	}
}

// A request waiting behind a running push gives up when its context ends,
// rather than holding the request past the server's write timeout.
func TestPush_WaitingGivesUpWithTheContext(t *testing.T) {
	s := New(&fakeLister{}, &fakeAgent{})
	s.lock <- struct{}{} // a push is running
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Push(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestNilSyncerDoesNothing(t *testing.T) {
	var s *Syncer
	if New(nil, &fakeAgent{}) != nil || New(&fakeLister{}, nil) != nil {
		t.Fatal("New with a missing dependency must return nil")
	}
	if err := s.Push(context.Background()); err != nil {
		t.Fatalf("nil Push: %v", err)
	}
	s.Run(context.Background(), nil) // returns at once
}
