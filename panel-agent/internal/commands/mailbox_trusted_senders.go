package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
)

// mailbox_trusted_senders.go — GH #2017. The senders a mailbox trusts, as
// contact cards in a "Trusted senders" address book of the mailbox's own
// account. Stalwart's spam filter (SpamSettings.trustContacts) does not
// treat mail as spam when the sender is on a card in one of the recipient's
// address books and the mail passes SPF or DMARC.
//
// Verified on Stalwart 0.16.24 (.60 test box, 2026-10-10): a card in a
// non-default book counts; the match ignores case on both sides; a +tag is
// a different address (a card for tag@ does not trust tag+news@); a sender
// that fails SPF and DMARC still lands in Junk.
//
// The panel sends the whole list. The verb manages only the cards it wrote,
// marked by their uid, wherever they are: a user's own contact at the same
// address is left alone, and a card of ours the user moved to another book
// is moved back.

const (
	trustedSenderUIDPrefix = "urn:jabali:trusted:"
	trustedSendersBookName = "Trusted senders"
	// trustedSendersMax bounds one call. The panel caps a mailbox at half
	// this, so two adds at once can't push a list over it.
	trustedSendersMax = 1000
	// trustedCardsMaxScan bounds the cards read from one account. An account
	// with more is refused rather than half-read: a card of ours past the
	// limit would stay trusted.
	trustedCardsMaxScan = 100000
)

type trustedSendersApplyParams struct {
	Email     string   `json:"email"`
	Addresses []string `json:"addresses"`
}

type trustedSendersApplyResult struct {
	Ok            bool   `json:"ok"`
	AccountID     string `json:"account_id,omitempty"`
	AddressBookID string `json:"address_book_id,omitempty"`
	Created       int    `json:"created"`
	Updated       int    `json:"updated"`
	Destroyed     int    `json:"destroyed"`
	// Skipped is true when the mailbox has no account in Stalwart yet and
	// there is nothing to trust, so there was nothing to do.
	Skipped bool `json:"skipped,omitempty"`
}

type trustedCard struct {
	ID             string                        `json:"id"`
	UID            string                        `json:"uid"`
	AddressBookIDs map[string]bool               `json:"addressBookIds"`
	Emails         map[string]directoryCardEmail `json:"emails"`
}

// email returns the card's first address, in key order.
func (c trustedCard) email() string {
	return directoryCard{Emails: c.Emails}.email()
}

func invalidTrustedArg(msg string) error {
	return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: msg}
}

// validate checks the params and dedupes the addresses in place. The panel
// sends canonical addresses from its rows, so anything else is refused
// rather than rewritten.
func (p *trustedSendersApplyParams) validate() error {
	local, dom, err := mailaddr.Canonicalise(p.Email)
	if err != nil || local+"@"+dom != p.Email {
		return invalidTrustedArg("email is not a canonical mailbox address")
	}
	// Never the domain directory's host: its book is shared with every
	// mailbox of the domain.
	if local == mailaddr.DirectoryLocalPart {
		return invalidTrustedArg("email is the domain directory's address")
	}
	if len(p.Addresses) > trustedSendersMax {
		return invalidTrustedArg(fmt.Sprintf("more than %d trusted senders", trustedSendersMax))
	}
	seen := make(map[string]bool, len(p.Addresses))
	out := make([]string, 0, len(p.Addresses))
	for _, a := range p.Addresses {
		canon, err := mailaddr.CanonicaliseSender(a)
		if err != nil {
			return invalidTrustedArg(fmt.Sprintf("invalid sender %q: %v", a, err))
		}
		if canon != a {
			return invalidTrustedArg(fmt.Sprintf("sender %q is not in canonical form", a))
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	p.Addresses = out
	return nil
}

func mailboxTrustedSendersApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, invalidTrustedArg("params required")
	}
	var p trustedSendersApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, invalidTrustedArg(fmt.Sprintf("parse params: %v", err))
	}
	if err := p.validate(); err != nil {
		return nil, err
	}

	accountID, err := accountIDByEmail(ctx, p.Email)
	if err != nil {
		return nil, err
	}
	if accountID == "" {
		if len(p.Addresses) == 0 {
			return trustedSendersApplyResult{Ok: true, Skipped: true}, nil
		}
		// Nobody has signed in to the mailbox yet. Make its account, as
		// mailbox.set_password does, so the first message is already trusted.
		if err := accountEnsureInRegistry(ctx, p.Email); err != nil {
			return nil, err
		}
		if accountID, err = accountIDByEmail(ctx, p.Email); err != nil {
			return nil, err
		}
		if accountID == "" {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "the mailbox's account is still missing after creating it"}
		}
	}
	if err := requireUserAccount(ctx, accountID); err != nil {
		return nil, err
	}

	cards, err := listTrustedCards(ctx, accountID)
	if err != nil {
		return nil, err
	}
	bookID, err := trustedSendersBook(ctx, accountID, len(p.Addresses) > 0)
	if err != nil {
		return nil, err
	}
	res := trustedSendersApplyResult{Ok: true, AccountID: accountID, AddressBookID: bookID}

	want := make(map[string]bool, len(p.Addresses))
	for _, a := range p.Addresses {
		want[a] = true
	}
	kept := make(map[string]bool, len(p.Addresses))
	update, destroy := map[string]any{}, map[string]any{}
	for _, c := range cards {
		addr, ours := strings.CutPrefix(c.UID, trustedSenderUIDPrefix)
		if !ours {
			continue // the user's own contact, or another verb's card
		}
		if !want[addr] || kept[addr] {
			destroy[c.ID] = nil
			continue
		}
		kept[addr] = true
		inBook := len(c.AddressBookIDs) == 1 && c.AddressBookIDs[bookID]
		if !inBook || len(c.Emails) != 1 || strings.ToLower(c.email()) != addr {
			update[c.ID] = trustedCardPatch(bookID, addr)
		}
	}
	create := map[string]any{}
	for _, a := range p.Addresses {
		if kept[a] {
			continue
		}
		card := trustedCardPatch(bookID, a)
		card["@type"], card["version"], card["uid"] = "Card", "1.0", trustedSenderUIDPrefix+a
		card["name"] = map[string]any{"full": a}
		create[fmt.Sprintf("t%d", len(create))] = card
	}

	// Removals first: when an add fails, a sender the panel no longer trusts
	// is already off the mail server.
	if res.Destroyed, err = directoryCardSet(ctx, accountID, "destroy", destroy); err != nil {
		return nil, err
	}
	if res.Updated, err = directoryCardSet(ctx, accountID, "update", update); err != nil {
		return nil, err
	}
	if res.Created, err = directoryCardSet(ctx, accountID, "create", create); err != nil {
		return nil, err
	}
	return res, nil
}

// trustedCardPatch is the part of a card the verb owns: its book and its
// one address.
func trustedCardPatch(bookID, addr string) map[string]any {
	return map[string]any{
		"addressBookIds": map[string]bool{bookID: true},
		"emails":         map[string]any{"e1": map[string]any{"address": addr}},
	}
}

// requireUserAccount refuses an account that is not a mailbox's: a mail
// group or a shared resource is a Group principal, and its address book is
// not the mailbox's to write.
func requireUserAccount(ctx context.Context, id string) error {
	var got struct {
		List []struct {
			ID   string `json:"id"`
			Type string `json:"@type"`
		} `json:"list"`
	}
	if err := jmapCall(ctx, "x:Account/get", map[string]any{"ids": []string{id}, "properties": []string{"@type"}}, &got); err != nil {
		return err
	}
	if len(got.List) != 1 || got.List[0].Type != "User" {
		return &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "the address belongs to an account that is not a mailbox"}
	}
	return nil
}

// trustedSendersBook returns the id of the account's "Trusted senders"
// book, creating it when create is set and there is none. "" means there
// is none and none was created.
func trustedSendersBook(ctx context.Context, accountID string, create bool) (string, error) {
	var got struct {
		List []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"list"`
	}
	if err := jmapCallWith(ctx, jmapCapContacts, "AddressBook/get", map[string]any{
		"accountId": accountID, "properties": []string{"name"},
	}, &got); err != nil {
		return "", err
	}
	var ids []string
	for _, b := range got.List {
		if b.Name == trustedSendersBookName {
			ids = append(ids, b.ID)
		}
	}
	if len(ids) > 0 {
		sort.Strings(ids)
		return ids[0], nil
	}
	if !create {
		return "", nil
	}
	var set jmapSetResult
	if err := jmapCallWith(ctx, jmapCapContacts, "AddressBook/set", map[string]any{
		"accountId": accountID,
		"create":    map[string]any{"trusted": map[string]any{"name": trustedSendersBookName}},
	}, &set); err != nil {
		return "", err
	}
	raw, ok := set.Created["trusted"]
	if !ok {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("AddressBook/set create refused: %s", string(set.NotCreated["trusted"]))}
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "AddressBook/set create returned no id"}
	}
	return created.ID, nil
}

// listTrustedCards returns every card in the account with the fields the
// verb compares. It reads them all: a card of ours can be in any book.
func listTrustedCards(ctx context.Context, accountID string) ([]trustedCard, error) {
	var ids []string
	for position := 0; ; {
		var q jmapQueryResult
		if err := jmapCallWith(ctx, jmapCapContacts, "ContactCard/query", map[string]any{
			"accountId": accountID, "position": position, "limit": mailDirectoryPage,
		}, &q); err != nil {
			return nil, err
		}
		ids = append(ids, q.IDs...)
		if len(ids) > trustedCardsMaxScan {
			return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: fmt.Sprintf("the mailbox has more than %d contacts", trustedCardsMaxScan)}
		}
		if len(q.IDs) < mailDirectoryPage {
			break
		}
		position += len(q.IDs)
	}
	cards := make([]trustedCard, 0, len(ids))
	for start := 0; start < len(ids); start += mailDirectoryPage {
		var got struct {
			List []trustedCard `json:"list"`
		}
		if err := jmapCallWith(ctx, jmapCapContacts, "ContactCard/get", map[string]any{
			"accountId": accountID, "ids": ids[start:min(start+mailDirectoryPage, len(ids))],
			"properties": []string{"uid", "addressBookIds", "emails"},
		}, &got); err != nil {
			return nil, err
		}
		cards = append(cards, got.List...)
	}
	return cards, nil
}

func init() {
	Default.Register("mailbox.trusted_senders.apply", mailboxTrustedSendersApplyHandler)
}
