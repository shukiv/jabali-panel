package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// trustFake is a stateful Stalwart for mailbox.trusted_senders.apply: one
// registry domain, accounts by local part, and the accounts' address books
// and cards.
type trustFake struct {
	accounts map[string]string // local part → account id
	types    map[string]string // account id → @type
	books    map[string]string // book id → name
	cards    map[string]trustedCard
	nextCard int
	nextBook int
	// ops records the ContactCard/set kinds in the order they ran.
	ops          []string
	bookCreates  int
	accountSets  int
	refuseCreate bool
	queryCalls   int
	// phantom adds that many ids after the real cards to every
	// ContactCard/query, for an account too big to hold in the fake.
	phantom int
}

func newTrustFake() *trustFake {
	return &trustFake{
		accounts: map[string]string{"me": "acct-me", "team": "acct-team"},
		types:    map[string]string{"acct-me": "User", "acct-team": "Group"},
		books:    map[string]string{"b0": "Stalwart Address Book (me@example.com)"},
		cards:    map[string]trustedCard{},
	}
}

func (f *trustFake) addCard(uid, email string, books ...string) string {
	f.nextCard++
	id := fmt.Sprintf("card%04d", f.nextCard)
	in := map[string]bool{}
	for _, b := range books {
		in[b] = true
	}
	f.cards[id] = trustedCard{ID: id, UID: uid, AddressBookIDs: in, Emails: map[string]directoryCardEmail{"e1": {Address: email}}}
	return id
}

func (f *trustFake) routes() map[string]jmapHandler {
	return map[string]jmapHandler{
		"x:Domain/query": jmapHandlerReturning(jmapQueryResult{IDs: []string{"dom1"}}),
		"x:Account/query": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				Filter struct {
					Name string `json:"name"`
				} `json:"filter"`
			}
			_ = json.Unmarshal(args, &a)
			if id, ok := f.accounts[a.Filter.Name]; ok {
				return jmapQueryResult{IDs: []string{id}}, nil
			}
			return jmapQueryResult{}, nil
		},
		"x:Account/set": func(args json.RawMessage) (any, *jmapFakeError) {
			f.accountSets++
			var a struct {
				Create map[string]struct {
					Name string `json:"name"`
					Type string `json:"@type"`
				} `json:"create"`
			}
			_ = json.Unmarshal(args, &a)
			res := jmapSetResult{Created: map[string]json.RawMessage{}}
			for k, c := range a.Create {
				id := "acct-" + c.Name
				f.accounts[c.Name], f.types[id] = id, c.Type
				res.Created[k] = json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))
			}
			return res, nil
		},
		"x:Account/get": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				IDs []string `json:"ids"`
			}
			_ = json.Unmarshal(args, &a)
			var list []map[string]string
			for _, id := range a.IDs {
				list = append(list, map[string]string{"id": id, "@type": f.types[id]})
			}
			return map[string]any{"list": list}, nil
		},
		"AddressBook/get": func(json.RawMessage) (any, *jmapFakeError) {
			var list []map[string]any
			for id, name := range f.books {
				list = append(list, map[string]any{"id": id, "name": name})
			}
			return map[string]any{"list": list}, nil
		},
		"AddressBook/set": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				Create map[string]struct {
					Name string `json:"name"`
				} `json:"create"`
			}
			_ = json.Unmarshal(args, &a)
			res := jmapSetResult{Created: map[string]json.RawMessage{}}
			for k, c := range a.Create {
				f.bookCreates++
				f.nextBook++
				id := fmt.Sprintf("tb%d", f.nextBook)
				f.books[id] = c.Name
				res.Created[k] = json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))
			}
			return res, nil
		},
		"ContactCard/query": func(args json.RawMessage) (any, *jmapFakeError) {
			f.queryCalls++
			var a struct {
				Position int `json:"position"`
				Limit    int `json:"limit"`
			}
			_ = json.Unmarshal(args, &a)
			ids := make([]string, 0, len(f.cards))
			for id := range f.cards {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			total := len(ids) + f.phantom
			if a.Position >= total {
				return jmapQueryResult{}, nil
			}
			page := make([]string, 0, a.Limit)
			for i := a.Position; i < min(a.Position+a.Limit, total); i++ {
				if i < len(ids) {
					page = append(page, ids[i])
				} else {
					page = append(page, fmt.Sprintf("phantom%06d", i))
				}
			}
			return jmapQueryResult{IDs: page}, nil
		},
		"ContactCard/get": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				IDs []string `json:"ids"`
			}
			_ = json.Unmarshal(args, &a)
			var list []trustedCard
			for _, id := range a.IDs {
				list = append(list, f.cards[id])
			}
			return map[string]any{"list": list}, nil
		},
		"ContactCard/set": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				Create  map[string]json.RawMessage `json:"create"`
				Update  map[string]json.RawMessage `json:"update"`
				Destroy []string                   `json:"destroy"`
			}
			_ = json.Unmarshal(args, &a)
			res := jmapSetResult{Created: map[string]json.RawMessage{}, Updated: map[string]json.RawMessage{}, NotCreated: map[string]json.RawMessage{}}
			if len(a.Create) > 0 {
				f.ops = append(f.ops, "create")
			}
			if len(a.Update) > 0 {
				f.ops = append(f.ops, "update")
			}
			if len(a.Destroy) > 0 {
				f.ops = append(f.ops, "destroy")
			}
			for k, raw := range a.Create {
				if f.refuseCreate {
					res.NotCreated[k] = json.RawMessage(`{"type":"forbidden"}`)
					continue
				}
				var c trustedCard
				_ = json.Unmarshal(raw, &c)
				books := make([]string, 0, len(c.AddressBookIDs))
				for b := range c.AddressBookIDs {
					books = append(books, b)
				}
				f.addCard(c.UID, c.email(), books...)
				res.Created[k] = json.RawMessage(`{"id":"x"}`)
			}
			for id, raw := range a.Update {
				var c trustedCard
				_ = json.Unmarshal(raw, &c)
				old := f.cards[id]
				if c.AddressBookIDs != nil {
					old.AddressBookIDs = c.AddressBookIDs
				}
				if c.Emails != nil {
					old.Emails = c.Emails
				}
				f.cards[id] = old
				res.Updated[id] = json.RawMessage(`null`)
			}
			for _, id := range a.Destroy {
				delete(f.cards, id)
				res.Destroyed = append(res.Destroyed, id)
			}
			return res, nil
		},
	}
}

// state returns every card as "uid|email|books", sorted.
func (f *trustFake) state() []string {
	var out []string
	for _, c := range f.cards {
		books := make([]string, 0, len(c.AddressBookIDs))
		for b := range c.AddressBookIDs {
			books = append(books, b)
		}
		sort.Strings(books)
		out = append(out, c.UID+"|"+c.email()+"|"+strings.Join(books, ","))
	}
	sort.Strings(out)
	return out
}

func runTrustedApply(t *testing.T, f *trustFake, email string, addresses []string) (trustedSendersApplyResult, error) {
	t.Helper()
	srv := newJMAPServer(t, f.routes())
	t.Cleanup(srv.Close)
	wireJMAP(t, srv)
	raw, _ := json.Marshal(map[string]any{"email": email, "addresses": addresses})
	out, err := mailboxTrustedSendersApplyHandler(context.Background(), raw)
	if err != nil {
		return trustedSendersApplyResult{}, err
	}
	return out.(trustedSendersApplyResult), nil
}

const trustPrefix = "urn:jabali:trusted:"

// The panel's cards become exactly the list, in the "Trusted senders" book:
// a missing one is created, a removed one destroyed, one the user moved to
// another book is moved back. The user's own contacts, even at a trusted
// address, are not touched.
func TestTrustedSendersApply_ConvergesThePanelsCards(t *testing.T) {
	f := newTrustFake()
	f.books["tb9"] = "Trusted senders"
	f.addCard(trustPrefix+"keep@x.com", "keep@x.com", "tb9")
	f.addCard(trustPrefix+"gone@x.com", "gone@x.com", "tb9")
	f.addCard(trustPrefix+"moved@x.com", "moved@x.com", "b0")
	f.addCard(trustPrefix+"edited@x.com", "someone-else@x.com", "tb9")
	f.addCard("urn:uuid:mine", "keep@x.com", "b0")
	f.addCard("urn:uuid:mine2", "gone@x.com", "b0")
	f.addCard("urn:jabali:directory:alice@example.com", "alice@example.com", "b0")

	res, err := runTrustedApply(t, f, "me@example.com", []string{"edited@x.com", "keep@x.com", "moved@x.com", "new@x.com"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []string{
		"urn:jabali:directory:alice@example.com|alice@example.com|b0",
		trustPrefix + "edited@x.com|edited@x.com|tb9",
		trustPrefix + "keep@x.com|keep@x.com|tb9",
		trustPrefix + "moved@x.com|moved@x.com|tb9",
		trustPrefix + "new@x.com|new@x.com|tb9",
		"urn:uuid:mine2|gone@x.com|b0",
		"urn:uuid:mine|keep@x.com|b0",
	}
	if got := f.state(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("cards:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if res.Created != 1 || res.Updated != 2 || res.Destroyed != 1 || res.AddressBookID != "tb9" || res.AccountID != "acct-me" {
		t.Errorf("result = %+v", res)
	}
	if f.bookCreates != 0 {
		t.Errorf("created a book although one is named Trusted senders")
	}

	// A second apply with the same list writes nothing.
	f.ops = nil
	if _, err := runTrustedApply(t, f, "me@example.com", []string{"edited@x.com", "keep@x.com", "moved@x.com", "new@x.com"}); err != nil {
		t.Fatal(err)
	}
	if len(f.ops) != 0 {
		t.Fatalf("steady apply wrote: %v", f.ops)
	}
}

// A card the panel wrote twice (a crash between create and stamp) keeps one.
func TestTrustedSendersApply_DuplicateCardDestroyed(t *testing.T) {
	f := newTrustFake()
	f.books["tb9"] = "Trusted senders"
	f.addCard(trustPrefix+"keep@x.com", "keep@x.com", "tb9")
	f.addCard(trustPrefix+"keep@x.com", "keep@x.com", "tb9")
	if _, err := runTrustedApply(t, f, "me@example.com", []string{"keep@x.com"}); err != nil {
		t.Fatal(err)
	}
	if got := f.state(); len(got) != 1 {
		t.Fatalf("cards = %v, want one", got)
	}
}

func TestTrustedSendersApply_CreatesTheBookWhenNeeded(t *testing.T) {
	f := newTrustFake()
	res, err := runTrustedApply(t, f, "me@example.com", []string{"bob@x.com"})
	if err != nil {
		t.Fatal(err)
	}
	if f.bookCreates != 1 || f.books[res.AddressBookID] != "Trusted senders" {
		t.Fatalf("books = %v, result %+v", f.books, res)
	}
	if got := f.state(); len(got) != 1 || got[0] != trustPrefix+"bob@x.com|bob@x.com|"+res.AddressBookID {
		t.Fatalf("cards = %v", got)
	}
}

// Removing the last sender destroys its card and creates no book.
func TestTrustedSendersApply_EmptyListCreatesNoBook(t *testing.T) {
	f := newTrustFake()
	f.addCard(trustPrefix+"gone@x.com", "gone@x.com", "b0")
	if _, err := runTrustedApply(t, f, "me@example.com", []string{}); err != nil {
		t.Fatal(err)
	}
	if f.bookCreates != 0 || len(f.cards) != 0 {
		t.Fatalf("book creates %d, cards %v", f.bookCreates, f.state())
	}
}

// Removals go first: when adding fails, a removed sender is still no longer
// trusted.
func TestTrustedSendersApply_RemovesBeforeItAdds(t *testing.T) {
	f := newTrustFake()
	f.books["tb9"] = "Trusted senders"
	f.addCard(trustPrefix+"gone@x.com", "gone@x.com", "tb9")
	f.refuseCreate = true
	if _, err := runTrustedApply(t, f, "me@example.com", []string{"new@x.com"}); err == nil {
		t.Fatal("a refused create must fail the apply")
	}
	if len(f.ops) == 0 || f.ops[0] != "destroy" {
		t.Fatalf("ops = %v, want destroy first", f.ops)
	}
	if len(f.cards) != 0 {
		t.Fatalf("cards = %v, want the removed card gone", f.state())
	}
}

// A card of ours whose address differs only in case is left as it is.
// Stalwart matches without case, so rewriting it would change nothing and
// write on every apply.
func TestTrustedSendersApply_CaseOnlyDifferenceNotRewritten(t *testing.T) {
	f := newTrustFake()
	f.books["tb9"] = "Trusted senders"
	f.addCard(trustPrefix+"keep@x.com", "Keep@X.com", "tb9")
	if _, err := runTrustedApply(t, f, "me@example.com", []string{"keep@x.com"}); err != nil {
		t.Fatal(err)
	}
	if len(f.ops) != 0 {
		t.Fatalf("rewrote a card that differs only in case: %v", f.ops)
	}
}

// An account with more contacts than the verb reads is refused before
// anything is written: a card of ours past the limit would stay trusted.
// An account at the limit is read whole.
func TestTrustedSendersApply_AccountTooBigToScanRefused(t *testing.T) {
	f := newTrustFake()
	f.phantom = trustedCardsMaxScan + 1
	_, err := runTrustedApply(t, f, "me@example.com", []string{"bob@x.com"})
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("err = %v, want failed precondition", err)
	}
	if len(f.ops) != 0 || f.bookCreates != 0 {
		t.Fatalf("wrote to an account it could not read whole: ops=%v books=%d", f.ops, f.bookCreates)
	}

	f = newTrustFake()
	f.phantom = trustedCardsMaxScan
	if _, err := runTrustedApply(t, f, "me@example.com", []string{"bob@x.com"}); err != nil {
		t.Fatalf("account at the limit: %v", err)
	}
}

// Every card of the account is looked at, past the first page.
func TestTrustedSendersApply_ScansEveryPage(t *testing.T) {
	f := newTrustFake()
	for i := 0; i < 1200; i++ {
		f.addCard(fmt.Sprintf("urn:uuid:%d", i), fmt.Sprintf("c%d@y.com", i), "b0")
	}
	f.addCard(trustPrefix+"gone@x.com", "gone@x.com", "b0") // sorts last
	if _, err := runTrustedApply(t, f, "me@example.com", []string{}); err != nil {
		t.Fatal(err)
	}
	if len(f.cards) != 1200 || f.queryCalls < 3 {
		t.Fatalf("%d cards left after %d queries; want the panel's card gone", len(f.cards), f.queryCalls)
	}
}

// A mailbox that never signed in has no account yet; one is made so the
// sender is trusted from the first message. With nothing to trust, no
// account is made.
func TestTrustedSendersApply_AccountNotYetInStalwart(t *testing.T) {
	f := newTrustFake()
	res, err := runTrustedApply(t, f, "fresh@example.com", []string{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || f.accountSets != 0 {
		t.Fatalf("empty list: result %+v, account creates %d", res, f.accountSets)
	}

	res, err = runTrustedApply(t, f, "fresh@example.com", []string{"bob@x.com"})
	if err != nil {
		t.Fatal(err)
	}
	if f.accountSets != 1 || res.AccountID != "acct-fresh" || res.Created != 1 {
		t.Fatalf("result %+v, account creates %d", res, f.accountSets)
	}
}

// The verb writes only into a mailbox's own account: never a mail group's
// or shared resource's (a Group), and never the domain directory's host.
func TestTrustedSendersApply_OnlyAUserAccount(t *testing.T) {
	f := newTrustFake()
	_, err := runTrustedApply(t, f, "team@example.com", []string{"bob@x.com"})
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("group account: err = %v", err)
	}
	if len(f.ops) != 0 || f.bookCreates != 0 {
		t.Fatalf("wrote into a group account: ops %v, books %d", f.ops, f.bookCreates)
	}
	if _, err := runTrustedApply(t, f, "jabali-directory@example.com", []string{"bob@x.com"}); !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("directory host: err = %v", err)
	}
}

func TestTrustedSendersApply_RefusesBadParams(t *testing.T) {
	tooMany := make([]string, 1001)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("s%d@x.com", i)
	}
	cases := []struct {
		name  string
		email string
		addrs []string
	}{
		{"mailbox not canonical", "Me@Example.com", []string{"bob@x.com"}},
		{"mailbox not an address", "me", []string{"bob@x.com"}},
		{"sender not canonical", "me@example.com", []string{"Bob@x.com"}},
		{"sender invalid", "me@example.com", []string{"bob@localhost"}},
		{"sender with a quote", "me@example.com", []string{"o'brien@x.com"}},
		{"too many", "me@example.com", tooMany},
	}
	for _, c := range cases {
		f := newTrustFake()
		_, err := runTrustedApply(t, f, c.email, c.addrs)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if len(f.ops) != 0 {
			t.Errorf("%s: wrote %v", c.name, f.ops)
		}
	}
	if _, err := mailboxTrustedSendersApplyHandler(context.Background(), nil); err == nil {
		t.Error("no params accepted")
	}
}

// The same address twice is one card.
func TestTrustedSendersApply_Dedupes(t *testing.T) {
	f := newTrustFake()
	res, err := runTrustedApply(t, f, "me@example.com", []string{"bob@x.com", "bob@x.com"})
	if err != nil || res.Created != 1 || len(f.cards) != 1 {
		t.Fatalf("res %+v err %v cards %v", res, err, f.state())
	}
}

func TestTrustedSendersApply_Registered(t *testing.T) {
	if !slices.Contains(Default.Commands(), "mailbox.trusted_senders.apply") {
		t.Fatal("verb not registered")
	}
}
