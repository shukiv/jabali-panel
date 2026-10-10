// Package trustedsenders sends a mailbox's trusted senders (GH #2017) to the
// agent. The API handler and the reconciler both use it, so the wire shape
// of mailbox.trusted_senders.apply lives in one place.
//
// The agent writes the list into Stalwart as contact cards in the mailbox's
// "Trusted senders" address book. Stalwart's spam filter does not treat mail
// as spam when its sender is on a card in one of the recipient's address
// books and passes SPF or DMARC (SpamSettings.trustContacts).
package trustedsenders

import (
	"context"
	"sort"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// AgentVerb converges a mailbox's trusted senders in Stalwart.
const AgentVerb = "mailbox.trusted_senders.apply"

// MaxPerMailbox caps a mailbox's list. The agent refuses a list over twice
// this, so rows a little over the cap (two adds at the same moment) still
// apply.
const MaxPerMailbox = 500

// Spec is the agent verb's params: the whole list, which replaces what the
// mailbox had.
type Spec struct {
	Email     string   `json:"email"`
	Addresses []string `json:"addresses"`
}

// Push sends email's whole list to the agent. An empty list removes every
// trusted sender. ctx bounds the call.
func Push(ctx context.Context, ag agent.AgentInterface, email string, addresses []string) error {
	if addresses == nil {
		addresses = []string{}
	}
	_, err := ag.Call(ctx, AgentVerb, Spec{Email: email, Addresses: addresses})
	return err
}

// Addresses returns the rows' addresses, sorted. It is never nil.
func Addresses(rows []models.MailboxTrustedSender) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Address)
	}
	sort.Strings(out)
	return out
}
