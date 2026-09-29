package mailaddrowner

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// fakeRegistry is Stalwart's registry: domains, and accounts with aliases.
type fakeRegistry struct {
	domains  map[string]string   // id → name
	accounts map[string]*account // id → account
	updates  []string            // "<account>:<patch key>"
	failOn   string              // account id whose update fails
	queryErr error
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		domains: map[string]string{"c3": "example.com", "c4": "other.example"},
		accounts: map[string]*account{
			"gc": {ID: "gc", EmailAddress: "ceo@example.com", Aliases: map[string]accountAlias{
				"0": {Name: "sales", DomainID: "c3"},
				"1": {Name: "info", DomainID: "c3"},
			}},
			"gd": {ID: "gd", EmailAddress: "newhire@example.com", Aliases: map[string]accountAlias{}},
			"ge": {ID: "ge", EmailAddress: "boss@other.example", Aliases: map[string]accountAlias{
				"0": {Name: "sales", DomainID: "c4"},
			}},
		},
	}
}

func (f *fakeRegistry) Query(_ context.Context, typeName string, _ map[string]any, _ []string) ([]json.RawMessage, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	var out []json.RawMessage
	switch typeName {
	case "Domain":
		for id, name := range f.domains {
			b, _ := json.Marshal(domain{ID: id, Name: name})
			out = append(out, b)
		}
	case "Account":
		for _, a := range f.accounts {
			b, _ := json.Marshal(a)
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeRegistry) Get(_ context.Context, typeName, id string) (json.RawMessage, error) {
	a, ok := f.accounts[id]
	if typeName != "Account" || !ok {
		return nil, errors.New("not found")
	}
	return json.Marshal(a)
}

// Update removes one alias the way Stalwart does: the aliases are a list
// keyed by position, so the ones after the removed entry move down one, and
// a key past the end is an invalid patch.
func (f *fakeRegistry) Update(_ context.Context, typeName, id string, payload any) error {
	if typeName != "Account" {
		return errors.New("unexpected type " + typeName)
	}
	if id == f.failOn {
		return errors.New("mail server down")
	}
	for k, v := range payload.(map[string]any) {
		key, ok := strings.CutPrefix(k, "aliases/")
		if v != nil || !ok {
			return errors.New("unexpected patch " + k)
		}
		a := f.accounts[id]
		if _, ok := a.Aliases[key]; !ok {
			return errors.New("invalidPatch: Invalid value for object property (" + k + ")")
		}
		n, _ := strconv.Atoi(key)
		next := map[string]accountAlias{}
		for i := 0; i < len(a.Aliases); i++ {
			switch {
			case i < n:
				next[strconv.Itoa(i)] = a.Aliases[strconv.Itoa(i)]
			case i > n:
				next[strconv.Itoa(i-1)] = a.Aliases[strconv.Itoa(i)]
			}
		}
		a.Aliases = next
		f.updates = append(f.updates, id+":"+k)
	}
	return nil
}

func (f *fakeRegistry) aliases(id string) []string {
	out := []string{}
	for _, al := range f.accounts[id].Aliases {
		out = append(out, al.Name+"@"+f.domains[al.DomainID])
	}
	sort.Strings(out)
	return out
}

type fakeOwners map[string]string

func (f fakeOwners) Owner(_ context.Context, address string) (string, error) { return f[address], nil }

// A mailbox created at an address the registry still holds as another
// account's alias would sign in to that account. Release takes the address
// off every account first, and only that address in that domain.
func TestRelease_TakesTheAddressOffEveryAccount(t *testing.T) {
	reg := newFakeRegistry()
	removed, err := Release(context.Background(), reg, "Sales@Example.com", "")
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(removed) != 1 || removed[0].AccountID != "gc" {
		t.Fatalf("removed = %+v, want sales@ off gc", removed)
	}
	if got := reg.aliases("gc"); !reflect.DeepEqual(got, []string{"info@example.com"}) {
		t.Fatalf("gc aliases = %v, want only info@", got)
	}
	if got := reg.aliases("ge"); !reflect.DeepEqual(got, []string{"sales@other.example"}) {
		t.Fatalf("the same local part in another domain must stay, got %v", got)
	}
}

// When an alias moves, the new owner keeps it.
func TestRelease_KeepsTheNewOwner(t *testing.T) {
	reg := newFakeRegistry()
	reg.accounts["gd"].Aliases["0"] = accountAlias{Name: "sales", DomainID: "c3"}
	if _, err := Release(context.Background(), reg, "sales@example.com", "NewHire@example.com"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := reg.aliases("gd"); !reflect.DeepEqual(got, []string{"sales@example.com"}) {
		t.Fatalf("new owner lost the alias: %v", got)
	}
	if got := reg.aliases("gc"); !reflect.DeepEqual(got, []string{"info@example.com"}) {
		t.Fatalf("old owner kept the alias: %v", got)
	}
}

func TestRelease_UnknownDomainIsHeldByNobody(t *testing.T) {
	reg := newFakeRegistry()
	removed, err := Release(context.Background(), reg, "sales@new.example", "")
	if err != nil || len(removed) != 0 || len(reg.updates) != 0 {
		t.Fatalf("got removed=%v err=%v updates=%v, want nothing", removed, err, reg.updates)
	}
}

// The create doors refuse the create when the release fails, so a failure
// must be reported, never swallowed.
func TestRelease_ReportsRegistryErrors(t *testing.T) {
	reg := newFakeRegistry()
	reg.failOn = "gc"
	if _, err := Release(context.Background(), reg, "sales@example.com", ""); err == nil {
		t.Fatal("a failed alias removal must be an error")
	}
	reg = newFakeRegistry()
	reg.queryErr = errors.New("unreachable")
	if _, err := Release(context.Background(), reg, "sales@example.com", ""); err == nil {
		t.Fatal("an unreadable registry must be an error")
	}
	if err := (Releaser{}).ReleaseAddress(context.Background(), "sales@example.com"); err == nil {
		t.Fatal("a releaser without a mail server client must refuse")
	}
	if _, err := Release(context.Background(), newFakeRegistry(), "not-an-address", ""); err == nil {
		t.Fatal("a malformed address must be an error")
	}
}

// Sweep takes an alias off an account when the database gives the address to
// someone else, and leaves an alias the database gives to no one.
func TestSweep_RemovesAliasesTheDatabaseGivesToSomeoneElse(t *testing.T) {
	reg := newFakeRegistry()
	owners := fakeOwners{
		"sales@example.com":   "newhire@example.com", // moved to newhire
		"info@example.com":    "info@example.com",    // now a mailbox of its own
		"sales@other.example": "",                    // deleted alias: nobody
	}
	removed, err := Sweep(context.Background(), reg, owners)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %+v, want sales@ and info@ off gc", removed)
	}
	if got := reg.aliases("gc"); len(got) != 0 {
		t.Fatalf("gc still holds %v", got)
	}
	if got := reg.aliases("ge"); !reflect.DeepEqual(got, []string{"sales@other.example"}) {
		t.Fatalf("an alias nobody owns must stay, got %v", got)
	}
	// The owner's own aliases stay.
	reg = newFakeRegistry()
	removed, err = Sweep(context.Background(), reg, fakeOwners{"sales@example.com": "CEO@example.com", "info@example.com": "ceo@example.com"})
	if err != nil || len(removed) != 0 {
		t.Fatalf("the owner's own aliases must stay: removed=%v err=%v", removed, err)
	}
}

// Stalwart renumbers an account's aliases when one is removed. Taking
// several off one account must still remove exactly the stale ones.
func TestSweep_RemovesSeveralAliasesFromOneAccount(t *testing.T) {
	reg := newFakeRegistry()
	names := []string{"a1", "keep1", "a2", "a3", "keep2", "a4", "a5"}
	reg.accounts["gc"].Aliases = map[string]accountAlias{}
	owners := fakeOwners{}
	for i, n := range names {
		reg.accounts["gc"].Aliases[strconv.Itoa(i)] = accountAlias{Name: n, DomainID: "c3"}
		if strings.HasPrefix(n, "keep") {
			owners[n+"@example.com"] = "ceo@example.com"
		} else {
			owners[n+"@example.com"] = n + "@example.com" // a mailbox of its own now
		}
	}
	removed, err := Sweep(context.Background(), reg, owners)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(removed) != 5 {
		t.Fatalf("removed %d aliases, want 5: %+v", len(removed), removed)
	}
	if got := reg.aliases("gc"); !reflect.DeepEqual(got, []string{"keep1@example.com", "keep2@example.com"}) {
		t.Fatalf("gc aliases = %v, want only keep1@ and keep2@", got)
	}
}

func TestValidKey(t *testing.T) {
	for _, k := range []string{"0", "12", "ab3"} {
		if !validKey(k) {
			t.Errorf("validKey(%q) = false", k)
		}
	}
	for _, k := range []string{"", "0/1", "a b", "../x", "0\"", "aliases/0"} {
		if validKey(k) {
			t.Errorf("validKey(%q) = true", k)
		}
	}
}
