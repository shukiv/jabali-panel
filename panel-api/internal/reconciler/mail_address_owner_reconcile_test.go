package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeAliasRegistry is Stalwart's registry: domains, and accounts with
// aliases, as stalwartadmin.Client returns them.
type fakeAliasRegistry struct {
	domains  map[string]string               // id → name
	accounts map[string]string               // id → emailAddress
	aliases  map[string]map[string][2]string // account id → key → {name, domainId}
	queries  []string                        // type names queried
	updates  []string                        // "<account id>:<patch key>"
	queryErr error
}

func (f *fakeAliasRegistry) Query(_ context.Context, typeName string, _ map[string]any, _ []string) ([]json.RawMessage, error) {
	f.queries = append(f.queries, typeName)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	var out []json.RawMessage
	switch typeName {
	case "Domain":
		for id, name := range f.domains {
			b, _ := json.Marshal(map[string]any{"id": id, "name": name})
			out = append(out, b)
		}
	case "Account":
		for id, email := range f.accounts {
			als := map[string]any{}
			for k, a := range f.aliases[id] {
				als[k] = map[string]any{"name": a[0], "domainId": a[1], "enabled": true}
			}
			b, _ := json.Marshal(map[string]any{"id": id, "emailAddress": email, "aliases": als})
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeAliasRegistry) Update(_ context.Context, typeName, id string, payload any) error {
	if typeName != "Account" {
		return errors.New("unexpected type " + typeName)
	}
	for k := range payload.(map[string]any) {
		key, _ := strings.CutPrefix(k, "aliases/")
		delete(f.aliases[id], key)
		f.updates = append(f.updates, id+":"+k)
	}
	return nil
}

type fakeAddressOwners map[string]string

func (f fakeAddressOwners) Owner(_ context.Context, address string) (string, error) {
	return f[address], nil
}

// The CEO's sales@ alias moved to the new hire and info@ became a mailbox of
// its own; the registry still has both on the CEO's account.
func staleAliasRegistry() *fakeAliasRegistry {
	return &fakeAliasRegistry{
		domains:  map[string]string{"c3": "example.com"},
		accounts: map[string]string{"gc": "ceo@example.com", "gd": "newhire@example.com"},
		aliases: map[string]map[string][2]string{
			"gc": {"0": {"sales", "c3"}, "1": {"info", "c3"}, "2": {"old", "c3"}},
			"gd": {"0": {"sales", "c3"}},
		},
	}
}

func staleAliasOwners() fakeAddressOwners {
	return fakeAddressOwners{
		"sales@example.com": "newhire@example.com",
		"info@example.com":  "info@example.com",
		// old@: a deleted alias the database gives to no one.
	}
}

func TestReconcileMailAddressOwners_TakesStaleAliasesOff(t *testing.T) {
	reg := staleAliasRegistry()
	r := (&Reconciler{log: slog.New(slog.DiscardHandler)}).WithMailAddressOwners(reg, staleAliasOwners())

	r.reconcileMailAddressOwners(context.Background())

	if _, ok := reg.aliases["gc"]["0"]; ok {
		t.Error("sales@ is still on the CEO's account after it moved to the new hire")
	}
	if _, ok := reg.aliases["gc"]["1"]; ok {
		t.Error("info@ is still on the CEO's account although it is a mailbox of its own")
	}
	if _, ok := reg.aliases["gc"]["2"]; !ok {
		t.Error("an alias the database gives to no one must be left alone")
	}
	if _, ok := reg.aliases["gd"]["0"]; !ok {
		t.Error("the new owner lost sales@")
	}
}

// The sweep reads every account; it runs once per interval, not every tick.
func TestReconcileMailAddressOwners_RunsOncePerInterval(t *testing.T) {
	reg := staleAliasRegistry()
	r := (&Reconciler{log: slog.New(slog.DiscardHandler)}).WithMailAddressOwners(reg, staleAliasOwners())

	r.reconcileMailAddressOwners(context.Background())
	n := len(reg.queries)
	r.reconcileMailAddressOwners(context.Background())
	if len(reg.queries) != n {
		t.Fatalf("a second pass inside the interval queried the registry again (%d → %d queries)", n, len(reg.queries))
	}
	r.mailAddrLastRun = time.Now().Add(-mailAddressSweepInterval - time.Second)
	r.reconcileMailAddressOwners(context.Background())
	if len(reg.queries) == n {
		t.Fatal("the sweep did not run again after the interval")
	}
}

func TestReconcileMailAddressOwners_NotWiredIsANoop(t *testing.T) {
	r := &Reconciler{log: slog.New(slog.DiscardHandler)}
	r.reconcileMailAddressOwners(context.Background()) // must not panic
	reg := staleAliasRegistry()
	r = (&Reconciler{log: slog.New(slog.DiscardHandler)}).WithMailAddressOwners(reg, nil)
	r.reconcileMailAddressOwners(context.Background())
	if len(reg.queries) != 0 {
		t.Fatalf("the sweep ran without an owner source: %v", reg.queries)
	}
}

// The relay identity is a mailbox too: it clears its address on the mail
// server before its row is written, and is not created when it cannot.
func TestReconcileSendmailCreds_RelayMailboxReleasesItsAddress(t *testing.T) {
	newRec := func(reg *fakeAliasRegistry) (*Reconciler, *fakeSendmailMailboxRepo) {
		mailboxes := &fakeSendmailMailboxRepo{
			byEmail:     map[string]*models.Mailbox{},
			domainNames: map[string]string{"d1": "site.tld"},
		}
		r := sendmailTestReconciler(&fakeSendmailAgent{}, mailboxes, []models.Domain{
			{ID: "d1", Name: "site.tld", UserID: "u1", EmailEnabled: true},
		})
		r.mailAddrRegistry = nil
		if reg != nil {
			r.mailAddrRegistry = reg
		}
		return r, mailboxes
	}

	reg := &fakeAliasRegistry{
		domains:  map[string]string{"c1": "site.tld"},
		accounts: map[string]string{"g1": "owner@site.tld"},
		aliases:  map[string]map[string][2]string{"g1": {"0": {"noreply", "c1"}}},
	}
	r, mailboxes := newRec(reg)
	r.reconcileSendmailCreds(context.Background())
	if len(mailboxes.created) != 1 {
		t.Fatalf("created %d relay mailboxes, want 1", len(mailboxes.created))
	}
	if _, ok := reg.aliases["g1"]["0"]; ok {
		t.Fatal("the relay's address is still another account's alias")
	}

	r, mailboxes = newRec(nil)
	r.reconcileSendmailCreds(context.Background())
	if len(mailboxes.created) != 0 {
		t.Fatalf("created %d relay mailboxes without a mail server client, want 0", len(mailboxes.created))
	}
}
