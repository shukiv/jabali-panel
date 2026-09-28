package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
)

// mail_directory.go — GH #1637, ADR-0171. A domain's directory: one read-only
// address book that lists the domain's mailboxes and is shared with them, so
// webmail and CardDAV clients suggest same-domain addresses. It replaces the
// server-wide Principal/query listing, which #1605 took away from mailboxes
// because it showed every domain on the server.
//
// The book belongs to a Group principal at jabali-directory@<domain>, the
// same host model the shared resources use (sharedresource_commands.go). The
// panel sends the whole desired state; this verb converges the host, the
// cards and the book's shareWith to it, so every call is idempotent.
//
// Verified on Stalwart 0.16.15: a reader granted mayRead sees the host as a
// shared account over JMAP and CardDAV, and every write it tries (card
// create, update, destroy; book rename, destroy) is refused with forbidden.
// A mailbox of another domain gets forbidden. The host is not a mail
// recipient (RCPT 550): delivery is resolved by the SQL directory.

// mailDirectoryUIDPrefix marks the cards this verb writes. A card's uid is
// the prefix plus the address it lists.
const mailDirectoryUIDPrefix = "urn:jabali:directory:"

const (
	// mailDirectoryMaxEntries bounds one call. A domain with more mailboxes
	// than this is refused rather than half-listed.
	mailDirectoryMaxEntries = 10000
	// mailDirectoryMaxName bounds a display name, in bytes.
	mailDirectoryMaxName = 255
	mailDirectoryPage    = 500 // ContactCard/query page and /get batch
	mailDirectorySetSize = 100 // cards per ContactCard/set call
)

type mailDirectoryEntry struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type mailDirectoryApplyParams struct {
	HostEmail   string               `json:"host_email"`
	DisplayName string               `json:"display_name"`
	Entries     []mailDirectoryEntry `json:"entries"`
	Readers     []string             `json:"readers"`
}

type mailDirectoryApplyResult struct {
	Ok                bool   `json:"ok"`
	HostAccountID     string `json:"host_account_id"`
	AddressBookID     string `json:"address_book_id"`
	Created           int    `json:"created"`
	Updated           int    `json:"updated"`
	Destroyed         int    `json:"destroyed"`
	Readers           int    `json:"readers"`
	ReadersUnresolved int    `json:"readers_unresolved"`
}

func invalidDirectoryArg(msg string) error {
	return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: msg}
}

// canonicalDirectoryAddress returns raw when it is a canonical address in
// domain, and an error otherwise. The panel sends addresses from its own
// rows, which are canonical, so anything else is refused rather than
// silently rewritten.
func canonicalDirectoryAddress(raw, domain string) (string, error) {
	local, dom, err := mailaddr.Canonicalise(raw)
	if err != nil {
		return "", invalidDirectoryArg(fmt.Sprintf("invalid address %q: %v", raw, err))
	}
	email := local + "@" + dom
	if email != raw {
		return "", invalidDirectoryArg(fmt.Sprintf("address %q is not in canonical form", raw))
	}
	if dom != domain {
		return "", invalidDirectoryArg(fmt.Sprintf("address %q is not in the directory's domain %s", raw, domain))
	}
	return email, nil
}

// cleanDirectoryName trims a display name and refuses control characters and
// over-long names.
func cleanDirectoryName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if len(name) > mailDirectoryMaxName {
		return "", invalidDirectoryArg("display name is too long")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", invalidDirectoryArg("display name contains a control character")
		}
	}
	return name, nil
}

// validate checks and normalises the params in place: entries and readers
// are deduplicated, names trimmed.
func (p *mailDirectoryApplyParams) validate() error {
	local, dom, err := mailaddr.Canonicalise(p.HostEmail)
	if err != nil || local+"@"+dom != p.HostEmail {
		return invalidDirectoryArg("host_email is not a canonical address")
	}
	// Only the directory's own host: a call can never point the verb at a
	// mailbox's or a shared resource's address book.
	if local != mailaddr.DirectoryLocalPart {
		return invalidDirectoryArg("host_email must be " + mailaddr.DirectoryLocalPart + "@<domain>")
	}
	if p.DisplayName, err = cleanDirectoryName(p.DisplayName); err != nil {
		return err
	}
	if len(p.Entries) > mailDirectoryMaxEntries || len(p.Readers) > mailDirectoryMaxEntries {
		return invalidDirectoryArg("too many entries or readers")
	}
	seen := make(map[string]bool, len(p.Entries))
	entries := make([]mailDirectoryEntry, 0, len(p.Entries))
	for _, e := range p.Entries {
		email, err := canonicalDirectoryAddress(e.Email, dom)
		if err != nil {
			return err
		}
		if email == p.HostEmail {
			return invalidDirectoryArg("the directory cannot list its own host")
		}
		name, err := cleanDirectoryName(e.Name)
		if err != nil {
			return err
		}
		if !seen[email] {
			seen[email] = true
			entries = append(entries, mailDirectoryEntry{Email: email, Name: name})
		}
	}
	p.Entries = entries
	seenReader := make(map[string]bool, len(p.Readers))
	readers := make([]string, 0, len(p.Readers))
	for _, r := range p.Readers {
		email, err := canonicalDirectoryAddress(r, dom)
		if err != nil {
			return err
		}
		if email == p.HostEmail {
			return invalidDirectoryArg("the directory's host cannot be a reader")
		}
		if !seenReader[email] {
			seenReader[email] = true
			readers = append(readers, email)
		}
	}
	p.Readers = readers
	return nil
}

func mailDirectoryApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, invalidDirectoryArg("params required")
	}
	var p mailDirectoryApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, invalidDirectoryArg(fmt.Sprintf("parse params: %v", err))
	}
	if err := p.validate(); err != nil {
		return nil, err
	}

	hostID, err := ensureGroupInRegistry(ctx, p.HostEmail, p.DisplayName)
	if err != nil {
		return nil, err
	}
	if err := requireGroupAccount(ctx, hostID); err != nil {
		return nil, err
	}
	bookID, err := ensureDirectoryBook(ctx, hostID, p.DisplayName)
	if err != nil {
		return nil, err
	}
	res := mailDirectoryApplyResult{Ok: true, HostAccountID: hostID, AddressBookID: bookID}
	if res.Created, res.Updated, res.Destroyed, err = syncDirectoryCards(ctx, hostID, bookID, p.Entries); err != nil {
		return nil, err
	}

	// Resolve every reader before pushing: shareWith is replaced whole, so a
	// reader lost to a lookup error would be revoked. An error fails the
	// apply and keeps the previous grants for the retry. The domain's
	// registry id is looked up once, so a reader costs one query, not two.
	domainID, err := directoryDomainID(ctx, p.HostEmail)
	if err != nil {
		return nil, err
	}
	shareWith := make(map[string]map[string]bool, len(p.Readers))
	for _, reader := range p.Readers {
		id, err := directoryReaderID(ctx, domainID, reader)
		if err != nil {
			return nil, err
		}
		if id == "" {
			res.ReadersUnresolved++
			continue
		}
		shareWith[id] = map[string]bool{"mayRead": true}
	}
	res.Readers = len(shareWith)

	// The name is what the owner and the admin see. Stalwart shows a reader
	// its own label for a shared book, whatever the owner named it.
	var set jmapSetResult
	if err := jmapCallWith(ctx, jmapCapContacts, "AddressBook/set", map[string]any{
		"accountId": hostID,
		"update": map[string]any{
			bookID: map[string]any{"name": p.DisplayName, "shareWith": shareWith},
		},
	}, &set); err != nil {
		return nil, err
	}
	if reason, ok := set.NotUpdated[bookID]; ok {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("AddressBook/set refused: %s", string(reason))}
	}
	return res, nil
}

// requireGroupAccount refuses a host that is not a Group principal. The
// address is reserved, but a mailbox imported with it would make
// ensureGroupInRegistry return that mailbox's own account, and the verb would
// then write into, and share, the mailbox owner's personal address book.
func requireGroupAccount(ctx context.Context, id string) error {
	var got struct {
		List []struct {
			ID   string `json:"id"`
			Type string `json:"@type"`
		} `json:"list"`
	}
	if err := jmapCall(ctx, "x:Account/get", map[string]any{"ids": []string{id}, "properties": []string{"@type"}}, &got); err != nil {
		return err
	}
	if len(got.List) != 1 || got.List[0].Type != "Group" {
		return &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "the directory address belongs to an account that is not the directory's host"}
	}
	return nil
}

// ensureDirectoryBook returns the host's address book, creating one when the
// host has none. Stalwart gives a new Group a default book, so the create is
// the fallback.
func ensureDirectoryBook(ctx context.Context, hostID, name string) (string, error) {
	id, err := defaultAddressBookID(ctx, hostID)
	if err != nil || id != "" {
		return id, err
	}
	if name == "" {
		name = "Directory"
	}
	var set jmapSetResult
	if err := jmapCallWith(ctx, jmapCapContacts, "AddressBook/set", map[string]any{
		"accountId": hostID,
		"create":    map[string]any{"dir": map[string]any{"name": name}},
	}, &set); err != nil {
		return "", err
	}
	raw, ok := set.Created["dir"]
	if !ok {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("AddressBook/set create refused: %s", string(set.NotCreated["dir"]))}
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "AddressBook/set create returned no id"}
	}
	return created.ID, nil
}

type directoryCardName struct {
	Full string `json:"full"`
}

type directoryCardEmail struct {
	Address string `json:"address"`
}

type directoryCard struct {
	ID     string                        `json:"id"`
	UID    string                        `json:"uid"`
	Name   *directoryCardName            `json:"name"`
	Emails map[string]directoryCardEmail `json:"emails"`
}

// email returns the card's first address, in key order.
func (c directoryCard) email() string {
	keys := make([]string, 0, len(c.Emails))
	for k := range c.Emails {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return c.Emails[keys[0]].Address
}

func (c directoryCard) fullName() string {
	if c.Name == nil {
		return ""
	}
	return c.Name.Full
}

// listDirectoryCards returns every card in the host account.
func listDirectoryCards(ctx context.Context, hostID string) ([]directoryCard, error) {
	var ids []string
	for position := 0; position <= 2*mailDirectoryMaxEntries; {
		var q jmapQueryResult
		if err := jmapCallWith(ctx, jmapCapContacts, "ContactCard/query", map[string]any{
			"accountId": hostID, "position": position, "limit": mailDirectoryPage,
		}, &q); err != nil {
			return nil, err
		}
		ids = append(ids, q.IDs...)
		if len(q.IDs) < mailDirectoryPage {
			break
		}
		position += len(q.IDs)
	}
	cards := make([]directoryCard, 0, len(ids))
	for start := 0; start < len(ids); start += mailDirectoryPage {
		var got struct {
			List []directoryCard `json:"list"`
		}
		if err := jmapCallWith(ctx, jmapCapContacts, "ContactCard/get", map[string]any{
			"accountId": hostID, "ids": ids[start:min(start+mailDirectoryPage, len(ids))],
			"properties": []string{"uid", "name", "emails"},
		}, &got); err != nil {
			return nil, err
		}
		cards = append(cards, got.List...)
	}
	return cards, nil
}

// directoryCardPatch is the part of a card the directory owns: its book, its
// address and its name (null when the mailbox has no display name).
func directoryCardPatch(bookID string, e mailDirectoryEntry) map[string]any {
	var name any
	if e.Name != "" {
		name = map[string]any{"full": e.Name}
	}
	return map[string]any{
		"addressBookIds": map[string]bool{bookID: true},
		"emails":         map[string]any{"e1": map[string]any{"address": e.Email}},
		"name":           name,
	}
}

// syncDirectoryCards converges the host's cards to entries: one card per
// entry, keyed by uid. A card that lists nothing wanted (a removed mailbox, a
// duplicate, a card this verb did not write) is destroyed.
func syncDirectoryCards(ctx context.Context, hostID, bookID string, entries []mailDirectoryEntry) (created, updated, destroyed int, err error) {
	existing, err := listDirectoryCards(ctx, hostID)
	if err != nil {
		return 0, 0, 0, err
	}
	want := make(map[string]mailDirectoryEntry, len(entries))
	for _, e := range entries {
		want[mailDirectoryUIDPrefix+e.Email] = e
	}
	kept := make(map[string]bool, len(existing))
	create, update, destroy := map[string]any{}, map[string]any{}, map[string]any{}
	for _, c := range existing {
		e, ok := want[c.UID]
		if !ok || kept[c.UID] {
			destroy[c.ID] = nil
			continue
		}
		kept[c.UID] = true
		if c.email() != e.Email || c.fullName() != e.Name {
			update[c.ID] = directoryCardPatch(bookID, e)
		}
	}
	for _, e := range entries {
		uid := mailDirectoryUIDPrefix + e.Email
		if kept[uid] {
			continue
		}
		card := directoryCardPatch(bookID, e)
		if e.Name == "" {
			delete(card, "name")
		}
		card["@type"], card["version"], card["uid"] = "Card", "1.0", uid
		create[fmt.Sprintf("c%d", len(create))] = card
	}

	if created, err = directoryCardSet(ctx, hostID, "create", create); err != nil {
		return created, 0, 0, err
	}
	if updated, err = directoryCardSet(ctx, hostID, "update", update); err != nil {
		return created, updated, 0, err
	}
	destroyed, err = directoryCardSet(ctx, hostID, "destroy", destroy)
	return created, updated, destroyed, err
}

// directoryCardSet sends one kind of ContactCard/set change in batches and
// reports how many Stalwart accepted. A refusal is an error, so the panel
// retries the whole apply; a card already gone counts as destroyed.
func directoryCardSet(ctx context.Context, hostID, op string, items map[string]any) (int, error) {
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	done := 0
	for start := 0; start < len(keys); start += mailDirectorySetSize {
		batch := keys[start:min(start+mailDirectorySetSize, len(keys))]
		args := map[string]any{"accountId": hostID}
		if op == "destroy" {
			args["destroy"] = batch
		} else {
			m := make(map[string]any, len(batch))
			for _, k := range batch {
				m[k] = items[k]
			}
			args[op] = m
		}
		var set jmapSetResult
		if err := jmapCallWith(ctx, jmapCapContacts, "ContactCard/set", args, &set); err != nil {
			return done, err
		}
		var refused map[string]json.RawMessage
		switch op {
		case "create":
			refused, done = set.NotCreated, done+len(set.Created)
		case "update":
			refused, done = set.NotUpdated, done+len(set.Updated)
		default:
			refused, done = set.NotDestroyed, done+len(set.Destroyed)
		}
		for k, reason := range refused {
			if op == "destroy" && strings.Contains(string(reason), `"notFound"`) {
				done++
				continue
			}
			return done, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("ContactCard/set %s %s refused: %s", op, k, string(reason))}
		}
	}
	return done, nil
}

// directoryDomainID returns the registry id of the host's domain. The host
// has just been ensured, so a missing domain is an error, not "no readers".
func directoryDomainID(ctx context.Context, hostEmail string) (string, error) {
	id, err := domainIDByName(ctx, hostEmail[strings.LastIndex(hostEmail, "@")+1:])
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "the directory's domain is not in the registry"}
	}
	return id, nil
}

// directoryAccountID finds the account with localPart in the registry
// domain domainID: the lookup accountIDByEmail makes, without its domain
// query. "" means there is none.
func directoryAccountID(ctx context.Context, localPart, domainID string) (string, error) {
	var result jmapQueryResult
	if err := jmapCall(ctx, "x:Account/query", map[string]any{
		"filter": map[string]any{"name": localPart, "domainId": domainID},
		"limit":  1,
	}, &result); err != nil {
		return "", err
	}
	if len(result.IDs) == 0 {
		return "", nil
	}
	return result.IDs[0], nil
}

// directoryReaderID resolves a reader's account id. A mailbox that has never
// signed in has no account in Stalwart's registry yet; it is created, as
// mailbox.set_password does, so the grant does not wait for a first login.
// "" means the account still could not be found. email is canonical and in
// the domain, as validate checked.
func directoryReaderID(ctx context.Context, domainID, email string) (string, error) {
	local := email[:strings.LastIndex(email, "@")]
	id, err := directoryAccountID(ctx, local, domainID)
	if err != nil || id != "" {
		return id, err
	}
	if err := accountEnsureInRegistry(ctx, email); err != nil {
		return "", err
	}
	return directoryAccountID(ctx, local, domainID)
}

func init() {
	Default.Register("mail.directory.apply", mailDirectoryApplyHandler)
}
