package userops

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// osTeardownLog is one ordered log of the host and row steps a cascade took,
// shared by the fake agent and the fake user repo.
type osTeardownLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *osTeardownLog) add(s string) {
	l.mu.Lock()
	l.steps = append(l.steps, s)
	l.mu.Unlock()
}

func (l *osTeardownLog) has(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, x := range l.steps {
		if x == s {
			return true
		}
	}
	return false
}

func (l *osTeardownLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.steps...)
}

// osTeardownAgent answers user.delete after delay (so a background call is
// still running when the cascade returns), with err; every other verb succeeds
// at once. A finished user.delete is logged as "user.delete".
type osTeardownAgent struct {
	log   *osTeardownLog
	delay time.Duration
	err   error
	block chan struct{} // when set, user.delete waits for it
}

func (a *osTeardownAgent) Call(ctx context.Context, method string, _ any) (json.RawMessage, error) {
	if method != "user.delete" {
		return json.RawMessage(`{}`), nil
	}
	if a.block != nil {
		<-a.block
	}
	time.Sleep(a.delay)
	if a.err != nil {
		return nil, a.err
	}
	a.log.add("user.delete")
	return json.RawMessage(`{}`), nil
}

type osTeardownUsers struct {
	repository.UserRepository
	log     *osTeardownLog
	deleted string
}

func (u *osTeardownUsers) Delete(_ context.Context, id string) error {
	u.deleted = id
	u.log.add("row")
	return nil
}

func osTarget() *models.User {
	return &models.User{ID: "u1", Username: strptr("alice")}
}

// A short-lived caller (the CLI) sets SyncOSTeardown: the OS account is gone
// before DeleteCascade returns, and it goes before the row does. Without it
// the CLI process exited before the background teardown ran, and the
// tenant's login-capable account and /home survived the delete.
func TestDeleteCascade_SyncOSTeardown_RemovesTheAccountBeforeReturning(t *testing.T) {
	log := &osTeardownLog{}
	ag := &osTeardownAgent{log: log, delay: 50 * time.Millisecond}
	users := &osTeardownUsers{log: log}

	err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{SyncOSTeardown: true}, osTarget(), "cli")
	if err != nil {
		t.Fatalf("cascade: %v", err)
	}
	got := log.snapshot()
	if len(got) != 2 || got[0] != "user.delete" || got[1] != "row" {
		t.Fatalf("steps = %v, want [user.delete row]: the OS account must be removed before the row, and before returning", got)
	}
}

// When the OS teardown fails, the row stays: it is the only handle the
// operator has to re-run the delete. The error says what happened.
func TestDeleteCascade_SyncOSTeardown_FailureKeepsTheRow(t *testing.T) {
	log := &osTeardownLog{}
	ag := &osTeardownAgent{log: log, err: errors.New("agent unavailable")}
	users := &osTeardownUsers{log: log}

	err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{SyncOSTeardown: true}, osTarget(), "cli")
	var ote *OSTeardownError
	if !errors.As(err, &ote) || ote.Username != "alice" {
		t.Fatalf("err = %v, want *OSTeardownError for alice", err)
	}
	if users.deleted != "" {
		t.Fatal("row deleted although the OS account is still on the host")
	}
}

// An account that is already gone (never provisioned, or removed by an earlier
// run whose row delete failed) counts as removed, so a re-run completes.
func TestDeleteCascade_SyncOSTeardown_MissingAccountCountsAsRemoved(t *testing.T) {
	log := &osTeardownLog{}
	ag := &osTeardownAgent{log: log, err: &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: `user "alice" does not exist`}}
	users := &osTeardownUsers{log: log}

	if err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{SyncOSTeardown: true}, osTarget(), "cli"); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if users.deleted != "u1" {
		t.Fatal("row kept although the OS account does not exist")
	}
}

// The long-running panel keeps the background teardown: a large home must not
// hold the HTTP request past its write timeout. The cascade returns while
// user.delete is still running, and the teardown still happens.
func TestDeleteCascade_DefaultOSTeardownRunsInTheBackground(t *testing.T) {
	log := &osTeardownLog{}
	ag := &osTeardownAgent{log: log, block: make(chan struct{})}
	users := &osTeardownUsers{log: log}

	done := make(chan error, 1)
	go func() {
		done <- DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{}, osTarget(), "api")
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cascade: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the default cascade waited for user.delete; the REST delete must not block on the OS teardown")
	}
	if users.deleted != "u1" {
		t.Fatal("row not deleted")
	}
	close(ag.block)
	deadline := time.Now().Add(2 * time.Second)
	for !log.has("user.delete") {
		if time.Now().After(deadline) {
			t.Fatal("background OS teardown never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
