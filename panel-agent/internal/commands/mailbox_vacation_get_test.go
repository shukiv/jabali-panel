package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mailbox.vacation.get resolves the account, reads the native VacationResponse
// singleton, and maps it to the snake_case wire shape the panel adopts from.
func TestMailboxVacationGet_ReadsEnabledNativeResponse(t *testing.T) {
	srv := newJMAPServer(t, map[string]jmapHandler{
		"x:Domain/query":  jmapHandlerReturning(jmapQueryResult{IDs: []string{"dom-1"}, Total: 1}),
		"x:Account/query": jmapHandlerReturning(jmapQueryResult{IDs: []string{"acct-1"}, Total: 1}),
		"VacationResponse/get": jmapHandlerReturning(jmapGetResult{List: []json.RawMessage{
			json.RawMessage(`{"id":"singleton","isEnabled":true,"fromDate":null,"toDate":null,"subject":"Away","textBody":"OOO","htmlBody":null}`),
		}}),
	})
	defer srv.Close()
	wireJMAP(t, srv)

	res, err := mailboxVacationGetHandler(context.Background(), json.RawMessage(`{"mailbox_email":"user@example.com"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got, ok := res.(mailboxVacationGetResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", res)
	}
	if !got.IsEnabled {
		t.Error("is_enabled = false, want true")
	}
	if got.Subject == nil || *got.Subject != "Away" {
		t.Errorf("subject = %v, want Away", got.Subject)
	}
	if got.TextBody == nil || *got.TextBody != "OOO" {
		t.Errorf("text_body = %v, want OOO", got.TextBody)
	}
}

// A mailbox whose account is not registered with Stalwart yet has nothing to
// adopt — the handler returns is_enabled=false rather than provisioning it.
func TestMailboxVacationGet_UnregisteredAccountIsNotEnabled(t *testing.T) {
	srv := newJMAPServer(t, map[string]jmapHandler{
		"x:Domain/query": jmapHandlerReturning(jmapQueryResult{IDs: nil, Total: 0}),
		// No x:Account/query or VacationResponse/get route — the handler must
		// short-circuit once the domain is absent.
	})
	defer srv.Close()
	wireJMAP(t, srv)

	res, err := mailboxVacationGetHandler(context.Background(), json.RawMessage(`{"mailbox_email":"ghost@fresh.example"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := res.(mailboxVacationGetResponse); got.IsEnabled {
		t.Error("is_enabled = true for an unregistered account, want false")
	}
}

func TestMailboxVacationGet_RequiresEmail(t *testing.T) {
	_, err := mailboxVacationGetHandler(context.Background(), json.RawMessage(`{"mailbox_email":""}`))
	requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
}
