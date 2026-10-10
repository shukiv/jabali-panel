package mailcreds

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var changed = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) string { return changed.Add(d).Format(time.RFC3339) }

// fakeRegistry is Stalwart's registry: accounts with their credentials.
type fakeRegistry struct {
	accounts map[string]*account
	updates  []string // "<account>:<patch key>"
	failOn   string   // account id whose update fails
	queryErr error
}

func (f *fakeRegistry) Query(_ context.Context, typeName string, _ map[string]any, _ []string) ([]json.RawMessage, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	if typeName != "Account" {
		return nil, errors.New("unexpected type " + typeName)
	}
	ids := make([]string, 0, len(f.accounts))
	for id := range f.accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []json.RawMessage
	for _, id := range ids {
		b, _ := json.Marshal(f.accounts[id])
		out = append(out, b)
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

// Update removes one credential the way Stalwart does: credentials are a
// list keyed by position, so the ones after the removed entry move down one,
// and a key past the end is an invalid patch.
func (f *fakeRegistry) Update(_ context.Context, typeName, id string, payload any) error {
	if typeName != "Account" {
		return errors.New("unexpected type " + typeName)
	}
	if id == f.failOn {
		return errors.New("mail server down")
	}
	for k, v := range payload.(map[string]any) {
		key, ok := strings.CutPrefix(k, "credentials/")
		if v != nil || !ok {
			return errors.New("unexpected patch " + k)
		}
		a := f.accounts[id]
		if _, ok := a.Credentials[key]; !ok {
			return errors.New("invalidPatch: Invalid value for object property (" + k + ")")
		}
		n, _ := strconv.Atoi(key)
		next := map[string]credential{}
		for i := 0; i < len(a.Credentials); i++ {
			switch {
			case i < n:
				next[strconv.Itoa(i)] = a.Credentials[strconv.Itoa(i)]
			case i > n:
				next[strconv.Itoa(i-1)] = a.Credentials[strconv.Itoa(i)]
			}
		}
		a.Credentials = next
		f.updates = append(f.updates, id+":"+k)
	}
	return nil
}

// left lists an account's credentials as "<type>@<createdAt>" in position
// order.
func (f *fakeRegistry) left(id string) []string {
	a := f.accounts[id]
	out := make([]string, len(a.Credentials))
	for i := range out {
		c := a.Credentials[strconv.Itoa(i)]
		out[i] = c.Type + "@" + c.CreatedAt
	}
	return out
}

func creds(cs ...credential) map[string]credential {
	m := map[string]credential{}
	for i, c := range cs {
		m[strconv.Itoa(i)] = c
	}
	return m
}

func password() credential { return credential{Type: "Password"} }
func appPassword(created string) credential {
	return credential{Type: "AppPassword", CreatedAt: created}
}
func apiKey(created string) credential { return credential{Type: "ApiKey", CreatedAt: created} }

// An app password made at or before the mailbox's password last changed is
// removed; one made after it stays, and so does the password itself.
func TestSweep_RemovesAppPasswordsMadeBeforeThePasswordChanged(t *testing.T) {
	reg := &fakeRegistry{accounts: map[string]*account{
		"n2": {ID: "n2", EmailAddress: "alice@example.com", Credentials: creds(
			password(), appPassword(at(-time.Hour)), appPassword(at(0)), appPassword(at(time.Hour)))},
	}}
	removed, err := Sweep(context.Background(), reg, map[string]time.Time{"alice@example.com": changed})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reg.left("n2"), []string{"Password@", "AppPassword@" + at(time.Hour)}; !reflect.DeepEqual(got, want) {
		t.Errorf("left %v, want %v", got, want)
	}
	if len(removed) != 2 || removed[0].Type != "AppPassword" || removed[0].Account != "alice@example.com" {
		t.Errorf("removed = %+v", removed)
	}
	// Each removal took the key from a fresh read: the second one was
	// renumbered to the first's position.
	if want := []string{"n2:credentials/1", "n2:credentials/1"}; !reflect.DeepEqual(reg.updates, want) {
		t.Errorf("updates = %v, want %v", reg.updates, want)
	}
}

// A mailbox that may not sign in (disabled, its owner suspended, its domain
// unverified, or no longer in the panel) keeps only its password.
func TestSweep_StripsAMailboxThatMayNotSignIn(t *testing.T) {
	reg := &fakeRegistry{accounts: map[string]*account{
		"n3": {ID: "n3", EmailAddress: "bob@example.com", Credentials: creds(
			appPassword(at(time.Hour)), password(), apiKey(at(time.Hour)))},
	}}
	if _, err := Sweep(context.Background(), reg, map[string]time.Time{"alice@example.com": changed}); err != nil {
		t.Fatal(err)
	}
	if got := reg.left("n3"); !reflect.DeepEqual(got, []string{"Password@"}) {
		t.Errorf("left %v", got)
	}
}

// API keys are removed everywhere: the panel offers no use for them.
func TestSweep_RemovesEveryAPIKey(t *testing.T) {
	reg := &fakeRegistry{accounts: map[string]*account{
		"n2": {ID: "n2", EmailAddress: "alice@example.com", Credentials: creds(password(), apiKey(at(time.Hour)))},
	}}
	removed, err := Sweep(context.Background(), reg, map[string]time.Time{"alice@example.com": changed})
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.left("n2"); !reflect.DeepEqual(got, []string{"Password@"}) || len(removed) != 1 || removed[0].Type != "ApiKey" {
		t.Errorf("left %v, removed %+v", got, removed)
	}
}

// Only app passwords and API keys are ever removed: a credential of any
// other type stays, whatever the mailbox.
func TestSweep_LeavesOtherCredentialTypes(t *testing.T) {
	reg := &fakeRegistry{accounts: map[string]*account{
		"n3": {ID: "n3", EmailAddress: "bob@example.com", Credentials: creds(
			password(), credential{Type: "Certificate"}, credential{Type: "SomethingNew", CreatedAt: at(-time.Hour)})},
	}}
	if _, err := Sweep(context.Background(), reg, map[string]time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(reg.updates) != 0 {
		t.Errorf("updates = %v", reg.updates)
	}
}

// An app password whose creation time can't be read is removed.
func TestSweep_AppPasswordWithoutACreationTimeIsRemoved(t *testing.T) {
	reg := &fakeRegistry{accounts: map[string]*account{
		"n2": {ID: "n2", EmailAddress: "alice@example.com", Credentials: creds(password(), appPassword(""), appPassword("yesterday"))},
	}}
	if _, err := Sweep(context.Background(), reg, map[string]time.Time{"alice@example.com": changed}); err != nil {
		t.Fatal(err)
	}
	if got := reg.left("n2"); !reflect.DeepEqual(got, []string{"Password@"}) {
		t.Errorf("left %v", got)
	}
}

// The account's address is compared without case.
func TestSweep_AddressesMatchWithoutCase(t *testing.T) {
	reg := &fakeRegistry{accounts: map[string]*account{
		"n2": {ID: "n2", EmailAddress: "Alice@Example.COM", Credentials: creds(password(), appPassword(at(time.Hour)))},
	}}
	if _, err := Sweep(context.Background(), reg, map[string]time.Time{"alice@example.com": changed}); err != nil {
		t.Fatal(err)
	}
	if len(reg.updates) != 0 {
		t.Errorf("updates = %v", reg.updates)
	}
}

// A registry that can't be read changes nothing; a failed removal stops the
// sweep and reports what it removed before.
func TestSweep_RegistryErrors(t *testing.T) {
	reg := &fakeRegistry{queryErr: errors.New("mail server down"), accounts: map[string]*account{}}
	if _, err := Sweep(context.Background(), reg, map[string]time.Time{}); err == nil {
		t.Error("no error from an unreadable registry")
	}

	reg = &fakeRegistry{failOn: "n3", accounts: map[string]*account{
		"n2": {ID: "n2", EmailAddress: "alice@example.com", Credentials: creds(password(), apiKey(at(0)))},
		"n3": {ID: "n3", EmailAddress: "bob@example.com", Credentials: creds(password(), apiKey(at(0)))},
	}}
	removed, err := Sweep(context.Background(), reg, map[string]time.Time{"alice@example.com": changed})
	if err == nil || len(removed) != 1 || removed[0].AccountID != "n2" {
		t.Errorf("removed %+v, err %v", removed, err)
	}
}

type fakeLogins struct {
	logins map[string]time.Time
	err    error
}

func (f fakeLogins) ListMailLogins(context.Context) (map[string]time.Time, error) {
	return f.logins, f.err
}

// The sweeper reads who may sign in first; when it can't, it removes nothing
// (it would otherwise strip every mailbox).
func TestSweeper_ReadsTheLoginsFirst(t *testing.T) {
	reg := &fakeRegistry{accounts: map[string]*account{
		"n2": {ID: "n2", EmailAddress: "alice@example.com", Credentials: creds(password(), appPassword(at(time.Hour)))},
	}}
	s := Sweeper{Registry: reg, Logins: fakeLogins{err: errors.New("db down")}}
	if _, err := s.SweepMailCredentials(context.Background()); err == nil || len(reg.updates) != 0 {
		t.Errorf("err %v, updates %v", err, reg.updates)
	}
	s.Logins = fakeLogins{logins: map[string]time.Time{"alice@example.com": changed}}
	if _, err := s.SweepMailCredentials(context.Background()); err != nil || len(reg.updates) != 0 {
		t.Errorf("err %v, updates %v", err, reg.updates)
	}
	if _, err := (Sweeper{}).SweepMailCredentials(context.Background()); err == nil {
		t.Error("an unwired sweeper reported success")
	}
}
