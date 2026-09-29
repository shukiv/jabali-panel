// Package mailaddrowner keeps each mail address with one owner in Stalwart's
// registry.
//
// Stalwart copies every account's addresses from the SQL directory
// (queryEmailAliases) into its own registry the first time an address
// resolves, and a later sync (every sign-in) adds addresses but never removes
// one. Delivery and sign-in look an address up in the registry first. So once
// sales@example.com has been an alias of ceo@example.com, the registry keeps
// it on the CEO's account after the alias is deleted or moved: mail to it goes
// on to the CEO, and a mailbox created later at sales@example.com signs in to
// the CEO's account (verified on Stalwart 0.16, 2026-09-29).
//
// Release removes an address from the registry before it gets a new owner;
// the mailbox create doors call it and refuse the create when it fails.
// Sweep removes, on every account, each alias the database gives to a
// different owner, which heals registries that already hold stale aliases.
package mailaddrowner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Registry is the part of stalwartadmin.Client this package uses.
type Registry interface {
	Query(ctx context.Context, typeName string, filter map[string]any, properties []string) ([]json.RawMessage, error)
	Update(ctx context.Context, typeName, id string, payload any) error
}

// Owners reports who owns an address in the panel's database.
type Owners interface {
	// Owner returns the address of the principal that address belongs to:
	// the mailbox at the address, else the mailbox an enabled alias at the
	// address delivers to, else the mail group at the address. It returns ""
	// when nothing in the database owns the address.
	Owner(ctx context.Context, address string) (string, error)
}

// Removal is one alias taken off one registry account.
type Removal struct {
	AccountID string // Stalwart account id
	Account   string // the account's own address
	Address   string // the alias that was removed
}

type account struct {
	ID           string                  `json:"id"`
	EmailAddress string                  `json:"emailAddress"`
	Aliases      map[string]accountAlias `json:"aliases"`
}

type accountAlias struct {
	Name     string `json:"name"`
	DomainID string `json:"domainId"`
}

type domain struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Release removes address from the aliases of every registry account except
// the one whose own address is keep ("" removes it everywhere). It returns
// the aliases it removed. An address whose domain Stalwart does not know is
// held by nobody, so Release does nothing for it.
func Release(ctx context.Context, reg Registry, address, keep string) ([]Removal, error) {
	local, domainName, err := splitAddress(address)
	if err != nil {
		return nil, err
	}
	domains, err := loadDomains(ctx, reg)
	if err != nil {
		return nil, err
	}
	domainID := ""
	for id, name := range domains {
		if name == domainName {
			domainID = id
			break
		}
	}
	if domainID == "" {
		return nil, nil
	}
	accounts, err := loadAccounts(ctx, reg)
	if err != nil {
		return nil, err
	}
	var removed []Removal
	for _, a := range accounts {
		if keep != "" && strings.EqualFold(a.EmailAddress, keep) {
			continue
		}
		for key, al := range a.Aliases {
			if al.DomainID != domainID || !strings.EqualFold(al.Name, local) {
				continue
			}
			if err := removeAlias(ctx, reg, a.ID, key); err != nil {
				return removed, err
			}
			removed = append(removed, Removal{AccountID: a.ID, Account: a.EmailAddress, Address: address})
		}
	}
	return removed, nil
}

// Sweep removes each registry alias that the database gives to a different
// owner. An alias the database gives to no one (a deleted alias, or an
// address the SQL directory resolves some other way) is left alone: it
// reaches nobody new, and Release clears it before the address gets a
// mailbox. It stops at the first registry error and returns what it removed
// so far.
func Sweep(ctx context.Context, reg Registry, owners Owners) ([]Removal, error) {
	domains, err := loadDomains(ctx, reg)
	if err != nil {
		return nil, err
	}
	accounts, err := loadAccounts(ctx, reg)
	if err != nil {
		return nil, err
	}
	ownerOf := map[string]string{}
	var removed []Removal
	for _, a := range accounts {
		for key, al := range a.Aliases {
			domainName, ok := domains[al.DomainID]
			if !ok || al.Name == "" {
				continue
			}
			address := strings.ToLower(al.Name) + "@" + domainName
			owner, seen := ownerOf[address]
			if !seen {
				if owner, err = owners.Owner(ctx, address); err != nil {
					return removed, fmt.Errorf("mailaddrowner: owner of %s: %w", address, err)
				}
				ownerOf[address] = owner
			}
			if owner == "" || strings.EqualFold(owner, a.EmailAddress) {
				continue
			}
			if err := removeAlias(ctx, reg, a.ID, key); err != nil {
				return removed, err
			}
			removed = append(removed, Removal{AccountID: a.ID, Account: a.EmailAddress, Address: address})
		}
	}
	return removed, nil
}

func loadDomains(ctx context.Context, reg Registry) (map[string]string, error) {
	raws, err := reg.Query(ctx, "Domain", nil, []string{"name"})
	if err != nil {
		return nil, fmt.Errorf("mailaddrowner: list domains: %w", err)
	}
	out := make(map[string]string, len(raws))
	for _, raw := range raws {
		var d domain
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("mailaddrowner: decode domain: %w", err)
		}
		out[d.ID] = strings.ToLower(strings.TrimSuffix(d.Name, "."))
	}
	return out, nil
}

func loadAccounts(ctx context.Context, reg Registry) ([]account, error) {
	raws, err := reg.Query(ctx, "Account", nil, []string{"emailAddress", "aliases"})
	if err != nil {
		return nil, fmt.Errorf("mailaddrowner: list accounts: %w", err)
	}
	out := make([]account, 0, len(raws))
	for _, raw := range raws {
		var a account
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("mailaddrowner: decode account: %w", err)
		}
		out = append(out, a)
	}
	return out, nil
}

// removeAlias drops one entry of an account's aliases map. A JMAP patch
// that sets aliases to an empty value is refused by Stalwart; a patch that
// nulls one key works (verified on 0.16).
func removeAlias(ctx context.Context, reg Registry, accountID, key string) error {
	if !validKey(key) {
		return fmt.Errorf("mailaddrowner: unexpected alias key %q on account %s", key, accountID)
	}
	if err := reg.Update(ctx, "Account", accountID, map[string]any{"aliases/" + key: nil}); err != nil {
		return fmt.Errorf("mailaddrowner: remove alias %s from account %s: %w", key, accountID, err)
	}
	return nil
}

// validKey accepts the keys Stalwart uses in an aliases map (short
// alphanumeric tokens), so a patch path is never built from anything else.
func validKey(key string) bool {
	if key == "" || len(key) > 32 {
		return false
	}
	for _, r := range key {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

var errBadAddress = errors.New("mailaddrowner: not a mail address")

func splitAddress(address string) (local, domainName string, err error) {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return "", "", fmt.Errorf("%w: %q", errBadAddress, address)
	}
	return address[:at], strings.TrimSuffix(address[at+1:], "."), nil
}

// Releaser releases an address before it gets a new owner. The mailbox create
// doors hold it as a mailboxops.AddressReleaser.
type Releaser struct {
	Registry Registry
}

// ReleaseAddress removes address from every registry account's aliases.
func (r Releaser) ReleaseAddress(ctx context.Context, address string) error {
	if r.Registry == nil {
		return errors.New("mailaddrowner: no mail server client")
	}
	_, err := Release(ctx, r.Registry, address, "")
	return err
}

// ReleaseTo removes address from every registry account's aliases except
// owner's. The alias create doors call it, best effort, when an alias moves.
func (r Releaser) ReleaseTo(ctx context.Context, address, owner string) error {
	if r.Registry == nil {
		return errors.New("mailaddrowner: no mail server client")
	}
	_, err := Release(ctx, r.Registry, address, owner)
	return err
}
