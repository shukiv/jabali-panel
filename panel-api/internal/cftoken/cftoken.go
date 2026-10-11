// Package cftoken picks the Cloudflare API tokens that may write an ACME
// DNS-01 challenge (GH #2032), in the order dns01.AuthoritativeRoute tries
// them: the domain owner's own token, then the server's.
//
// Isolation rule: a user's token is offered only for a name that user owns
// (the nearest domains row at or above the name) on a certificate that user
// owns (the lineage's own domains row). Subdomain delegation (GH #1812) and
// admin-created rows can nest one tenant's domain under another's, so both
// checks are needed: the first keeps A's token off B's names, the second
// keeps B's token off A's certificate when it carries one of B's names.
package cftoken

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// Source says whose token a candidate is, for logs. Never log Token.
type Source string

const (
	SourceOwner  Source = "owner"
	SourceServer Source = "server"
)

// Candidate is one token to try.
type Candidate struct {
	Source Source
	Token  string
}

// Set is the ordered candidates for one name.
type Set struct {
	Candidates []Candidate
	// Version changes whenever the tokens on offer change (the owner saves,
	// replaces or removes theirs, the server token changes), so a route
	// decision cached under another version is stale.
	Version string
}

// DomainFinder is the slice of repository.DomainRepository the resolver needs.
type DomainFinder interface {
	FindByName(ctx context.Context, name string) (*models.Domain, error)
}

// Resolver reads the stored tokens. A nil Key means no token can be
// unsealed: the set is empty, as before any token was configured.
type Resolver struct {
	Domains DomainFinder
	Tokens  repository.UserCloudflareTokenRepository
	Key     *ssokey.Key
}

// For returns the candidates for name on the certificate lineage (the
// certbot --cert-name). An empty lineage — a renewal conf written before
// GH #2032 — offers the server token only.
//
// A failed read leaves that token out, keeps the others and returns the
// error: the caller may use the set but must not cache a decision made
// from it.
func (r Resolver) For(ctx context.Context, srv *models.ServerSettings, lineage, name string) (Set, error) {
	var set Set
	if r.Key == nil {
		set.Version = "nokey"
		return set, nil
	}
	var errs []error

	ownerVer := "-"
	var uid string
	if r.Tokens != nil {
		var err error
		if uid, err = r.certOwner(ctx, canonical(lineage), canonical(name)); err != nil {
			errs = append(errs, err)
		}
	}
	if uid != "" {
		row, err := r.Tokens.Get(ctx, uid)
		switch {
		case errors.Is(err, repository.ErrNotFound):
		case err != nil:
			errs = append(errs, fmt.Errorf("read the domain owner's Cloudflare token: %w", err))
		default:
			ownerVer = uid + "@" + strconv.FormatInt(row.UpdatedAt.UnixNano(), 10)
			if tok, err := r.Key.Open(row.TokenEnc); err != nil {
				errs = append(errs, fmt.Errorf("unseal the domain owner's Cloudflare token: %w", err))
			} else {
				set.Candidates = append(set.Candidates, Candidate{Source: SourceOwner, Token: string(tok)})
			}
		}
	}

	serverVer := "-"
	if srv != nil && len(srv.CFAPITokenEnc) > 0 {
		sum := sha256.Sum256(srv.CFAPITokenEnc)
		serverVer = hex.EncodeToString(sum[:8])
		if tok, err := r.Key.Open(srv.CFAPITokenEnc); err != nil {
			errs = append(errs, fmt.Errorf("unseal the server's Cloudflare token: %w", err))
		} else {
			set.Candidates = append(set.Candidates, Candidate{Source: SourceServer, Token: string(tok)})
		}
	}

	set.Version = "owner=" + ownerVer + ";server=" + serverVer
	return set, errors.Join(errs...)
}

// certOwner returns the user whose token may be offered for name on
// lineage's certificate, or "" when no one qualifies (see the package
// comment).
func (r Resolver) certOwner(ctx context.Context, lineage, name string) (string, error) {
	if lineage == "" || name == "" || r.Domains == nil {
		return "", nil
	}
	nameOwner, err := r.nearestOwner(ctx, name)
	if err != nil || nameOwner == "" {
		return "", err
	}
	d, err := r.Domains.FindByName(ctx, lineage)
	if errors.Is(err, repository.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up domain %s: %w", lineage, err)
	}
	if d.UserID != nameOwner {
		return "", nil
	}
	return nameOwner, nil
}

// nearestOwner walks name's labels up to the nearest domains row:
// "www.sub.example.com" tries itself, "sub.example.com", "example.com".
// A failed lookup stops the walk — a row further up is not the owner when
// a nearer one might exist.
func (r Resolver) nearestOwner(ctx context.Context, name string) (string, error) {
	labels := strings.Split(name, ".")
	for i := 0; i < len(labels)-1; i++ {
		candidate := strings.Join(labels[i:], ".")
		d, err := r.Domains.FindByName(ctx, candidate)
		if err == nil {
			return d.UserID, nil
		}
		if !errors.Is(err, repository.ErrNotFound) {
			return "", fmt.Errorf("look up domain %s: %w", candidate, err)
		}
	}
	return "", nil
}

func canonical(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	return strings.TrimSuffix(strings.TrimPrefix(s, "*."), ".")
}
