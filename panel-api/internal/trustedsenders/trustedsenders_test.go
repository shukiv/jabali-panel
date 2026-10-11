package trustedsenders

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakeAgent struct {
	cmd    string
	params json.RawMessage
	err    error
}

func (a *fakeAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.cmd = cmd
	a.params, _ = json.Marshal(params)
	return json.RawMessage(`{"ok":true}`), a.err
}

// Push sends the mailbox's whole list to the agent verb, as a JSON array
// even when it is empty: an empty list is how the last sender is removed.
func TestPush_SendsTheWholeList(t *testing.T) {
	ag := &fakeAgent{}
	if err := Push(context.Background(), ag, "me@x.com", []string{"a@y.com", "b@z.com"}); err != nil {
		t.Fatal(err)
	}
	if ag.cmd != AgentVerb || AgentVerb != "mailbox.trusted_senders.apply" {
		t.Fatalf("cmd = %q", ag.cmd)
	}
	if got := string(ag.params); got != `{"email":"me@x.com","addresses":["a@y.com","b@z.com"]}` {
		t.Fatalf("params = %s", got)
	}

	if err := Push(context.Background(), ag, "me@x.com", nil); err != nil {
		t.Fatal(err)
	}
	if got := string(ag.params); got != `{"email":"me@x.com","addresses":[]}` {
		t.Fatalf("empty list params = %s", got)
	}
}

func TestPush_ReturnsTheAgentError(t *testing.T) {
	boom := errors.New("agent down")
	if err := Push(context.Background(), &fakeAgent{err: boom}, "me@x.com", nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestAddresses(t *testing.T) {
	got := Addresses([]models.MailboxTrustedSender{{Address: "b@z.com"}, {Address: "a@y.com"}})
	if len(got) != 2 || got[0] != "a@y.com" || got[1] != "b@z.com" {
		t.Fatalf("Addresses = %v, want sorted", got)
	}
	if got := Addresses(nil); got == nil || len(got) != 0 {
		t.Fatalf("Addresses(nil) = %#v, want an empty non-nil slice", got)
	}
}

// The API's cap sits under the agent's, so a few rows over it (two adds at
// once) never make every apply for the mailbox fail.
func TestMaxPerMailbox(t *testing.T) {
	if MaxPerMailbox != 500 {
		t.Fatalf("MaxPerMailbox = %d", MaxPerMailbox)
	}
}
