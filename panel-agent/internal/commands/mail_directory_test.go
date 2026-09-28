package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dirFake is a stateful Stalwart for mail.directory.apply: one registry
// domain, accounts by local part, and the host's address book and cards.
type dirFake struct {
	accounts   map[string]string // local part → account id
	types      map[string]string // account id → @type
	unfindable map[string]bool   // local parts x:Account/query never finds
	queryErr   map[string]bool   // local parts whose x:Account/query fails
	cards      map[string]directoryCard
	nextCard   int
	cardSets   int
	shareWith  map[string]map[string]bool
	bookName   string
	bookSets   int
	// domainQueries counts x:Domain/query calls.
	domainQueries int
}

func newDirFake() *dirFake {
	return &dirFake{
		accounts:   map[string]string{"jabali-directory": "host1", "alice": "acct-alice"},
		types:      map[string]string{"host1": "Group", "acct-alice": "User"},
		unfindable: map[string]bool{},
		queryErr:   map[string]bool{},
		cards:      map[string]directoryCard{},
	}
}

func (f *dirFake) addCard(uid, email, name string) {
	f.nextCard++
	id := fmt.Sprintf("card%d", f.nextCard)
	c := directoryCard{ID: id, UID: uid, Emails: map[string]directoryCardEmail{"e1": {Address: email}}}
	if name != "" {
		c.Name = &directoryCardName{Full: name}
	}
	f.cards[id] = c
}

func (f *dirFake) routes() map[string]jmapHandler {
	return map[string]jmapHandler{
		"x:Domain/query": func(json.RawMessage) (any, *jmapFakeError) {
			f.domainQueries++
			return jmapQueryResult{IDs: []string{"dom1"}}, nil
		},
		"x:Account/query": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				Filter struct {
					Name string `json:"name"`
				} `json:"filter"`
			}
			_ = json.Unmarshal(args, &a)
			if f.queryErr[a.Filter.Name] {
				return nil, &jmapFakeError{Type: "serverFail"}
			}
			if id, ok := f.accounts[a.Filter.Name]; ok && !f.unfindable[a.Filter.Name] {
				return jmapQueryResult{IDs: []string{id}}, nil
			}
			return jmapQueryResult{}, nil
		},
		"x:Account/set": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				Create map[string]struct {
					Name string `json:"name"`
					Type string `json:"@type"`
				} `json:"create"`
			}
			_ = json.Unmarshal(args, &a)
			res := jmapSetResult{Created: map[string]json.RawMessage{}, NotCreated: map[string]json.RawMessage{}}
			for k, c := range a.Create {
				if _, ok := f.accounts[c.Name]; ok {
					res.NotCreated[k] = json.RawMessage(`{"type":"alreadyExists"}`)
					continue
				}
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
		"AddressBook/get": jmapHandlerReturning(map[string]any{"list": []map[string]any{{"id": "book1", "isDefault": true}}}),
		"AddressBook/set": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				Update map[string]struct {
					Name      string                     `json:"name"`
					ShareWith map[string]map[string]bool `json:"shareWith"`
				} `json:"update"`
			}
			_ = json.Unmarshal(args, &a)
			f.bookSets++
			u := a.Update["book1"]
			f.bookName, f.shareWith = u.Name, u.ShareWith
			return jmapSetResult{Updated: map[string]json.RawMessage{"book1": json.RawMessage(`null`)}}, nil
		},
		"ContactCard/query": func(json.RawMessage) (any, *jmapFakeError) {
			ids := make([]string, 0, len(f.cards))
			for id := range f.cards {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			return jmapQueryResult{IDs: ids}, nil
		},
		"ContactCard/get": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				IDs []string `json:"ids"`
			}
			_ = json.Unmarshal(args, &a)
			var list []directoryCard
			for _, id := range a.IDs {
				list = append(list, f.cards[id])
			}
			return map[string]any{"list": list}, nil
		},
		"ContactCard/set": func(args json.RawMessage) (any, *jmapFakeError) {
			f.cardSets++
			var a struct {
				Create  map[string]json.RawMessage `json:"create"`
				Update  map[string]json.RawMessage `json:"update"`
				Destroy []string                   `json:"destroy"`
			}
			_ = json.Unmarshal(args, &a)
			res := jmapSetResult{Created: map[string]json.RawMessage{}, Updated: map[string]json.RawMessage{}}
			for k, raw := range a.Create {
				var c directoryCard
				_ = json.Unmarshal(raw, &c)
				f.addCard(c.UID, c.email(), c.fullName())
				res.Created[k] = json.RawMessage(`{"id":"x"}`)
			}
			for id, raw := range a.Update {
				var c directoryCard
				_ = json.Unmarshal(raw, &c)
				old := f.cards[id]
				old.Emails, old.Name = c.Emails, c.Name
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

// state returns the host's cards as "email|name|uid", sorted.
func (f *dirFake) state() []string {
	var out []string
	for _, c := range f.cards {
		out = append(out, c.email()+"|"+c.fullName()+"|"+c.UID)
	}
	sort.Strings(out)
	return out
}

func runDirectoryApply(t *testing.T, f *dirFake, params map[string]any) (mailDirectoryApplyResult, error) {
	t.Helper()
	srv := newJMAPServer(t, f.routes())
	t.Cleanup(srv.Close)
	wireJMAP(t, srv)
	raw, _ := json.Marshal(params)
	out, err := mailDirectoryApplyHandler(context.Background(), raw)
	if err != nil {
		return mailDirectoryApplyResult{}, err
	}
	return out.(mailDirectoryApplyResult), nil
}

func directoryParams() map[string]any {
	return map[string]any{
		"host_email":   "jabali-directory@example.com",
		"display_name": "example.com directory",
		"entries": []map[string]string{
			{"email": "alice@example.com", "name": "Alice New"},
			{"email": "bob@example.com", "name": ""},
		},
		"readers": []string{"alice@example.com", "bob@example.com"},
	}
}

// GH #1637: the host's cards become exactly the entries — a renamed mailbox
// is updated, a new one created, a removed one and a card the directory did
// not write are destroyed — and the book is shared read-only with every
// reader, including one that has never signed in.
func TestMailDirectoryApply_ConvergesCardsAndGrants(t *testing.T) {
	f := newDirFake()
	f.addCard("urn:jabali:directory:alice@example.com", "alice@example.com", "Alice Old")
	f.addCard("urn:jabali:directory:carol@example.com", "carol@example.com", "Carol")
	f.addCard("urn:uuid:foreign", "x@evil.example", "Foreign")

	res, err := runDirectoryApply(t, f, directoryParams())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []string{
		"alice@example.com|Alice New|urn:jabali:directory:alice@example.com",
		"bob@example.com||urn:jabali:directory:bob@example.com",
	}
	if got := f.state(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("cards:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if res.Created != 1 || res.Updated != 1 || res.Destroyed != 2 {
		t.Errorf("counts = %+v, want created 1, updated 1, destroyed 2", res)
	}
	wantShare := map[string]map[string]bool{"acct-alice": {"mayRead": true}, "acct-bob": {"mayRead": true}}
	if fmt.Sprint(f.shareWith) != fmt.Sprint(wantShare) {
		t.Errorf("shareWith = %v, want %v (read only, bob registered on the way)", f.shareWith, wantShare)
	}
	if f.bookName != "example.com directory" || res.Readers != 2 || res.ReadersUnresolved != 0 || res.HostAccountID != "host1" {
		t.Errorf("book name %q, result %+v", f.bookName, res)
	}
}

// The domain is looked up a fixed number of times, not once per reader: a
// large domain's apply must fit the panel's time budget.
func TestMailDirectoryApply_DomainLookupsDoNotGrowWithReaders(t *testing.T) {
	f := newDirFake()
	p := directoryParams()
	var readers []string
	for i := 0; i < 20; i++ {
		local := fmt.Sprintf("user%02d", i)
		f.accounts[local] = "acct-" + local
		readers = append(readers, local+"@example.com")
	}
	p["readers"] = readers
	if _, err := runDirectoryApply(t, f, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if f.domainQueries > 3 {
		t.Fatalf("x:Domain/query ran %d times for 20 readers, want a fixed few", f.domainQueries)
	}
	if len(f.shareWith) != 20 {
		t.Fatalf("shareWith has %d readers, want 20", len(f.shareWith))
	}
}

// A second apply of the same state changes no card.
func TestMailDirectoryApply_IsIdempotent(t *testing.T) {
	f := newDirFake()
	if _, err := runDirectoryApply(t, f, directoryParams()); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	sets := f.cardSets
	res, err := runDirectoryApply(t, f, directoryParams())
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if f.cardSets != sets || res.Created+res.Updated+res.Destroyed != 0 {
		t.Fatalf("second apply sent %d ContactCard/set calls, result %+v", f.cardSets-sets, res)
	}
}

// A host address held by a mailbox (an import that bypassed the reserved
// name) is refused before anything is written: the verb would otherwise
// fill and share that mailbox's personal address book.
func TestMailDirectoryApply_RefusesAHostThatIsNotAGroup(t *testing.T) {
	f := newDirFake()
	f.types["host1"] = "User"
	_, err := runDirectoryApply(t, f, directoryParams())
	requireAgentErrorCode(t, err, agentwire.CodeFailedPrecondition)
	if f.cardSets != 0 || f.bookSets != 0 {
		t.Fatalf("wrote to a non-Group host: %d card sets, %d book sets", f.cardSets, f.bookSets)
	}
}

// A reader lookup that fails must not push a share map without that reader,
// which would revoke it; the apply fails and the old grants stay.
func TestMailDirectoryApply_ReaderLookupErrorKeepsTheGrants(t *testing.T) {
	f := newDirFake()
	f.queryErr["bob"] = true
	if _, err := runDirectoryApply(t, f, directoryParams()); err == nil {
		t.Fatal("a reader lookup error must fail the apply")
	}
	if f.bookSets != 0 {
		t.Fatal("the share map was pushed without the reader whose lookup failed")
	}
}

// A reader Stalwart still cannot resolve after the registry create is
// reported and left out; the others are granted.
func TestMailDirectoryApply_UnresolvedReaderIsReported(t *testing.T) {
	f := newDirFake()
	f.unfindable["bob"] = true
	res, err := runDirectoryApply(t, f, directoryParams())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ReadersUnresolved != 1 || res.Readers != 1 || len(f.shareWith) != 1 || !f.shareWith["acct-alice"]["mayRead"] {
		t.Fatalf("result %+v shareWith %v", res, f.shareWith)
	}
}

// Every refused input is refused before Stalwart is called.
func TestMailDirectoryApply_ValidatesInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("Stalwart was called for an input that must be refused")
		http.Error(w, "no", http.StatusBadRequest)
	}))
	defer srv.Close()
	wireJMAP(t, srv)

	cases := map[string]func(p map[string]any){
		// carol is neither an entry nor a reader, so only the host rule can refuse it.
		"host is a mailbox address": func(p map[string]any) { p["host_email"] = "carol@example.com" },
		"host not canonical":        func(p map[string]any) { p["host_email"] = "Jabali-Directory@example.com" },
		"entry in another domain":   func(p map[string]any) { p["entries"] = []map[string]string{{"email": "eve@other.example"}} },
		"entry not canonical":       func(p map[string]any) { p["entries"] = []map[string]string{{"email": "Alice@example.com"}} },
		"entry is the host":         func(p map[string]any) { p["entries"] = []map[string]string{{"email": "jabali-directory@example.com"}} },
		"entry name control char": func(p map[string]any) {
			p["entries"] = []map[string]string{{"email": "alice@example.com", "name": "A\x07"}}
		},
		"entry name too long": func(p map[string]any) {
			p["entries"] = []map[string]string{{"email": "alice@example.com", "name": strings.Repeat("a", 256)}}
		},
		"reader in another domain":    func(p map[string]any) { p["readers"] = []string{"eve@other.example"} },
		"reader is the host":          func(p map[string]any) { p["readers"] = []string{"jabali-directory@example.com"} },
		"reader with shell metachars": func(p map[string]any) { p["readers"] = []string{"a;b@example.com"} },
		"display name control char":   func(p map[string]any) { p["display_name"] = "x\ny" },
		"too many readers": func(p map[string]any) {
			r := make([]string, mailDirectoryMaxEntries+1)
			for i := range r {
				r[i] = fmt.Sprintf("u%d@example.com", i)
			}
			p["readers"] = r
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := directoryParams()
			mutate(p)
			raw, _ := json.Marshal(p)
			_, err := mailDirectoryApplyHandler(context.Background(), raw)
			requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
		})
	}
}
