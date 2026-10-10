// Package mailcreds keeps the mail server's app passwords and API keys in
// line with the panel's mailboxes.
//
// Stalwart reads each mailbox's password from the panel's database (the SQL
// directory), but it keeps the app passwords and API keys a mailbox creates
// (webmail, Settings > Security) in its own registry and checks them there.
// So they went on working after the panel changed the mailbox's password,
// disabled the mailbox or suspended its owner (verified on Stalwart 0.16.24).
// Turning app passwords off on Stalwart's User role is not an option: webmail
// sign-in from the panel creates a short-lived app password on the mailbox.
//
// Sweep removes, on every registry account:
//   - every API key (the panel has no use for them, and Stalwart's User role
//     no longer lets a mailbox create one);
//   - every app password, when the account is not a mailbox that may sign in;
//   - each app password created at or before the mailbox's password last
//     changed.
//
// A credential of any other type, the mailbox's password included, is never
// touched.
package mailcreds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Registry is the part of stalwartadmin.Client this package uses.
type Registry interface {
	Query(ctx context.Context, typeName string, filter map[string]any, properties []string) ([]json.RawMessage, error)
	Get(ctx context.Context, typeName, id string) (json.RawMessage, error)
	Update(ctx context.Context, typeName, id string, payload any) error
}

// Logins lists the mailboxes that may sign in (repository.MailLoginRepository):
// lower-cased address → when its password last changed.
type Logins interface {
	ListMailLogins(ctx context.Context) (map[string]time.Time, error)
}

// Removal is one credential taken off one registry account.
type Removal struct {
	AccountID string // Stalwart account id
	Account   string // the account's address
	Type      string // AppPassword or ApiKey
	Reason    string
}

// The credential types the sweep removes.
const (
	typeAppPassword = "AppPassword"
	typeAPIKey      = "ApiKey"
)

type credential struct {
	Type      string `json:"@type"`
	CreatedAt string `json:"createdAt,omitempty"`
}

type account struct {
	ID           string                `json:"id"`
	EmailAddress string                `json:"emailAddress"`
	Credentials  map[string]credential `json:"credentials"`
}

// mustGo says whether credential c goes from an account and why. mayLogin is
// whether the account is a mailbox that may sign in; changedAt is when its
// password last changed.
func mustGo(c credential, mayLogin bool, changedAt time.Time) (string, bool) {
	switch c.Type {
	case typeAPIKey:
		return "API key", true
	case typeAppPassword:
		if !mayLogin {
			return "the mailbox may not sign in", true
		}
		created, err := time.Parse(time.RFC3339, c.CreatedAt)
		if err != nil {
			return "no readable creation time", true
		}
		if !created.After(changedAt) {
			return "made before the password last changed", true
		}
	}
	return "", false
}

// Sweep removes, on every registry account, the credentials mustGo names.
// logins is the database's list of mailboxes that may sign in. It stops at
// the first registry error and returns what it removed so far.
func Sweep(ctx context.Context, reg Registry, logins map[string]time.Time) ([]Removal, error) {
	raws, err := reg.Query(ctx, "Account", nil, []string{"emailAddress", "credentials"})
	if err != nil {
		return nil, fmt.Errorf("mailcreds: list accounts: %w", err)
	}
	var removed []Removal
	for _, raw := range raws {
		var a account
		if err := json.Unmarshal(raw, &a); err != nil {
			return removed, fmt.Errorf("mailcreds: decode account: %w", err)
		}
		address := strings.ToLower(a.EmailAddress)
		changedAt, mayLogin := logins[address]
		if address == "" {
			mayLogin = false
		}
		if _, _, ok := next(a, mayLogin, changedAt); !ok {
			continue
		}
		got, err := strip(ctx, reg, a.ID, mayLogin, changedAt)
		removed = append(removed, got...)
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// next returns the first credential of a, by position, that must go.
func next(a account, mayLogin bool, changedAt time.Time) (string, credential, bool) {
	keys := make([]string, 0, len(a.Credentials))
	for k := range a.Credentials {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ni, ei := strconv.Atoi(keys[i])
		nj, ej := strconv.Atoi(keys[j])
		if ei != nil || ej != nil {
			return keys[i] < keys[j]
		}
		return ni < nj
	})
	for _, k := range keys {
		if _, ok := mustGo(a.Credentials[k], mayLogin, changedAt); ok {
			return k, a.Credentials[k], true
		}
	}
	return "", credential{}, false
}

// removeMu serializes credential removals in this process: the reconciler's
// sweep and the disable and suspend doors share the panel process, and two
// removals on one account at once could each shift the other's key.
var removeMu sync.Mutex

// strip removes from one account each credential that must go. Stalwart keys
// an account's credentials by position: removing one renumbers the ones
// after it, the same as its aliases (mailaddrowner). So the account is read
// again before each removal and the key taken from that read.
func strip(ctx context.Context, reg Registry, id string, mayLogin bool, changedAt time.Time) ([]Removal, error) {
	removeMu.Lock()
	defer removeMu.Unlock()
	var removed []Removal
	limit := -1
	for {
		raw, err := reg.Get(ctx, "Account", id)
		if err != nil {
			return removed, fmt.Errorf("mailcreds: read account %s: %w", id, err)
		}
		var a account
		if err := json.Unmarshal(raw, &a); err != nil {
			return removed, fmt.Errorf("mailcreds: decode account %s: %w", id, err)
		}
		if limit < 0 {
			limit = len(a.Credentials)
		}
		key, c, ok := next(a, mayLogin, changedAt)
		if !ok {
			return removed, nil
		}
		if len(removed) >= limit {
			return removed, fmt.Errorf("mailcreds: account %s still holds a credential to remove after %d removals", id, len(removed))
		}
		if !validKey(key) {
			return removed, fmt.Errorf("mailcreds: unexpected credential key %q on account %s", key, id)
		}
		if err := reg.Update(ctx, "Account", id, map[string]any{"credentials/" + key: nil}); err != nil {
			return removed, fmt.Errorf("mailcreds: remove credential %s from account %s: %w", key, id, err)
		}
		reason, _ := mustGo(c, mayLogin, changedAt)
		removed = append(removed, Removal{AccountID: id, Account: strings.ToLower(a.EmailAddress), Type: c.Type, Reason: reason})
	}
}

// validKey accepts the keys Stalwart uses for a credential's position, so a
// patch path is never built from anything else.
func validKey(key string) bool {
	if key == "" || len(key) > 9 {
		return false
	}
	for _, r := range key {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Sweeper runs a sweep against the database's current list of mailboxes
// that may sign in. The reconciler runs one every pass where that list
// changed; the doors that disable a mailbox or suspend a user run one at
// once.
type Sweeper struct {
	Registry Registry
	Logins   Logins
}

// SweepMailCredentials reads the mailboxes that may sign in and sweeps. When
// the list can't be read it removes nothing: an empty list would strip every
// mailbox.
func (s Sweeper) SweepMailCredentials(ctx context.Context) ([]Removal, error) {
	if s.Registry == nil || s.Logins == nil {
		return nil, errors.New("mailcreds: no mail server client")
	}
	logins, err := s.Logins.ListMailLogins(ctx)
	if err != nil {
		return nil, fmt.Errorf("mailcreds: list the mailboxes that may sign in: %w", err)
	}
	return Sweep(ctx, s.Registry, logins)
}
