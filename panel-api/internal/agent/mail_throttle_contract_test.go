package agent

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailthrottle"
)

// The wire is pinned as literal JSON, so renaming a field in
// internal/mailthrottle fails here: a panel and an agent from different
// releases talk during an update, and a silently renamed key would drop the
// sender a throttle is for.
func TestMailThrottles_ApplyWire(t *testing.T) {
	m := NewMockClient().On(mailthrottle.VerbApply, map[string]any{"stalwart_id": "jg1nyykmahqa", "changed": true})
	res, err := MailThrottles{Agent: m}.Apply(context.Background(), mailthrottle.ApplyRequest{
		StalwartID: "old1", Scope: "user", ScopeRef: "alice@example.com", Window: "day", Limit: 500,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.StalwartID != "jg1nyykmahqa" || !res.Changed {
		t.Fatalf("result = %+v", res)
	}
	calls := m.Calls()
	if len(calls) != 1 || calls[0].Command != "mail.throttle.apply" {
		t.Fatalf("calls = %+v", calls)
	}
	const want = `{"stalwart_id":"old1","scope":"user","scope_ref":"alice@example.com","window":"day","limit":500}`
	if got := string(calls[0].Params); got != want {
		t.Fatalf("params = %s\nwant     %s", got, want)
	}
}

func TestMailThrottles_ApplyRejectsAMissingID(t *testing.T) {
	m := NewMockClient().On(mailthrottle.VerbApply, map[string]any{"changed": true})
	if _, err := (MailThrottles{Agent: m}).Apply(context.Background(), mailthrottle.ApplyRequest{Scope: "global", Window: "hour", Limit: 1}); err == nil {
		t.Fatal("an answer without stalwart_id was accepted; the panel would stamp an empty id and create a duplicate next tick")
	}
}

func TestMailThrottles_DeleteWire(t *testing.T) {
	m := NewMockClient().On(mailthrottle.VerbDelete, map[string]any{"deleted": true})
	if err := (MailThrottles{Agent: m}).Delete(context.Background(), "jg1nyykmahqa"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	calls := m.Calls()
	if len(calls) != 1 || calls[0].Command != "mail.throttle.delete" || string(calls[0].Params) != `{"stalwart_id":"jg1nyykmahqa"}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestMailThrottles_ListWire(t *testing.T) {
	m := NewMockClient().On(mailthrottle.VerbList, map[string]any{
		"throttles": []map[string]string{{"stalwart_id": "jg1nyykmahqa", "description": "jabali global *: 10 per hour"}},
	})
	items, err := MailThrottles{Agent: m}.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].StalwartID != "jg1nyykmahqa" || items[0].Description != "jabali global *: 10 per hour" {
		t.Fatalf("items = %+v", items)
	}
	if calls := m.Calls(); len(calls) != 1 || calls[0].Command != "mail.throttle.list" {
		t.Fatalf("calls = %+v", calls)
	}
}
