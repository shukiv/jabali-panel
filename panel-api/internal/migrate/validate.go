package migrate

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ConflictKind enumerates pre-flight blockers. Operator must
// resolve every blocker (rename target user, free up the domain,
// or skip a colliding mailbox via Validate option) before restore
// can run.
type ConflictKind string

const (
	ConflictTargetUserExists ConflictKind = "target_user_exists"
	ConflictDomainTaken      ConflictKind = "domain_taken"
	ConflictUsernameInvalid  ConflictKind = "username_invalid"
)

// Conflict is one row in the validation report. The Detail is a
// short, operator-readable string suitable for surfacing in the
// admin UI's conflict list.
type Conflict struct {
	Kind   ConflictKind `json:"kind"`
	Detail string       `json:"detail"`
}

// ValidationReport summarises what restore would do + every
// blocker we found. Empty Blockers ↔ ready to restore.
type ValidationReport struct {
	TargetUser  string     `json:"target_user"`
	SourceUser  string     `json:"source_user"`
	SourceKind  string     `json:"source_kind"`
	Blockers    []Conflict `json:"blockers"`
	Projections struct {
		BytesTotal        int64 `json:"bytes_total"`
		DomainsToCreate   int   `json:"domains_to_create"`
		DBsToCreate       int   `json:"dbs_to_create"`
		MailboxesToCreate int   `json:"mailboxes_to_create"`
		CronToCreate      int   `json:"cron_to_create"`
		SSHKeysToCreate   int   `json:"ssh_keys_to_create"`
	} `json:"projections"`
	// Warnings forwarded from the manifest plus any new ones
	// the validator surfaced (e.g. quota projection > server cap).
	Warnings []Warning `json:"warnings"`
}

// ValidateDeps wires the existing repos so Validate can run
// against the live panel DB without depending on the full app.Deps.
type ValidateDeps struct {
	Users   repository.UserRepository
	Domains repository.DomainRepository
}

// Validate runs pre-flight conflict detection against an
// AccountManifest. Returns (report, nil) when complete (regardless
// of whether report.Blockers is empty); returns (nil, err) only on
// an unrecoverable infra error (DB down, etc.).
//
// targetUsername is the jabali-side username the operator picked.
//
// acceptExistingUserID is the optional ID of a user that's already
// expected to exist for this migration — set when the CLI's auto-
// create flow (jabali migrate import --target-email + --target-
// password) has already minted the destination user before validate
// runs. When the FindByUsername lookup returns a user with this ID,
// the target_user_exists conflict is suppressed (it's the user we
// just created, not a pre-existing one). Empty string preserves the
// strict 'must not exist' check for live-source flows where
// validate runs before user creation.
func Validate(ctx context.Context, deps ValidateDeps, m *AccountManifest, targetUsername string, acceptExistingUserID string, acceptExistingDomain bool) (*ValidationReport, error) {
	if m == nil {
		return nil, errors.New("validate: manifest nil")
	}
	if deps.Users == nil || deps.Domains == nil {
		return nil, errors.New("validate: deps not wired")
	}

	rpt := &ValidationReport{
		TargetUser: targetUsername,
		SourceUser: m.Source.User,
		SourceKind: m.Source.Kind,
		Warnings:   append([]Warning{}, m.Warnings...),
	}
	rpt.Projections.BytesTotal = m.Sizes.HomeBytes + m.Sizes.DBsBytes + m.Sizes.MailBytes + m.Sizes.LogsBytes
	rpt.Projections.DomainsToCreate = len(m.Domains)
	rpt.Projections.DBsToCreate = len(m.Databases)
	rpt.Projections.MailboxesToCreate = len(m.Mailboxes)
	rpt.Projections.CronToCreate = len(m.Cron)
	rpt.Projections.SSHKeysToCreate = len(m.SSH)

	// Username sanity. POSIX-compatible, 1-32 chars, start with a-z,
	// then lowercase alnum / hyphen / underscore — refuse anything
	// that wouldn't survive a useradd.
	if !isValidUnixUsername(targetUsername) {
		rpt.Blockers = append(rpt.Blockers, Conflict{
			Kind:   ConflictUsernameInvalid,
			Detail: fmt.Sprintf("target username %q must be 1-32 chars, start with a-z, then lowercase alnum, hyphen or underscore", targetUsername),
		})
	}

	// Target user existence check. Strict mode (acceptExistingUserID
	// == "") refuses any pre-existing user with the chosen username;
	// auto-create mode (acceptExistingUserID set) accepts the user
	// when its ID matches what the migration_jobs row recorded
	// during pre-validate user creation. Non-matching ID still
	// conflicts — operator picked a username that collides with
	// some other user.
	if u, err := deps.Users.FindByUsername(ctx, targetUsername); err == nil && u != nil {
		if acceptExistingUserID == "" || u.ID != acceptExistingUserID {
			rpt.Blockers = append(rpt.Blockers, Conflict{
				Kind:   ConflictTargetUserExists,
				Detail: fmt.Sprintf("user %q already exists in jabali; pick a different target username or delete the existing one", targetUsername),
			})
		}
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("validate: lookup target user: %w", err)
	}

	// Each domain must NOT exist anywhere in jabali (table has
	// global UNIQUE on name).
	for _, d := range m.Domains {
		existing, err := deps.Domains.FindByName(ctx, d.Name)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("validate: lookup domain %q: %w", d.Name, err)
		}
		if existing != nil {
			// GH #646: a refresh (acceptExistingDomain) may legitimately target
			// the account's OWN domain. Accept it ONLY when it belongs to the
			// SAME target user — never let a refresh touch another tenant's domain.
			if acceptExistingDomain && acceptExistingUserID != "" && existing.UserID == acceptExistingUserID {
				continue
			}
			rpt.Blockers = append(rpt.Blockers, Conflict{
				Kind:   ConflictDomainTaken,
				Detail: fmt.Sprintf("domain %q already registered in jabali (owned by user %s); free it first or skip this domain in the manifest", d.Name, existing.UserID),
			})
		}
	}

	return rpt, nil
}

// isValidUnixUsername mirrors the rules useradd applies on Debian:
// start with a lowercase letter, then lowercase / digits / hyphen /
// underscore, 1..32 chars total. GH #746: this used to reject
// underscore, but the agent's looksLikeUnixUsername allows it (and
// creates such users), and the Plesk auto-kick slug produces one
// (demolab.test -> demolab_test) — so the mismatch failed imports at
// validate AFTER the account already existed. Underscore is a valid
// useradd char, so accept it and stay consistent with the agent.
func isValidUnixUsername(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		// GH #746: underscore is a valid unix username char (useradd allows
		// it) and the agent's looksLikeUnixUsername accepts it — so the agent
		// happily CREATES an underscore user, then this validate stage rejected
		// it, failing the import after the account already exists. It's also
		// what the Plesk auto-kick slug produces (demolab.test -> demolab_test).
		// Allow it here to match the agent + actual unix rules.
		case r == '_':
		default:
			return false
		}
	}
	return true
}
