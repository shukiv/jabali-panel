// Package dirprivops holds the per-directory password-protection (cPanel
// "Directory Privacy", M50) lifecycle shared by the HTTP handler
// (internal/api/domain_directory_privacy.go) and the CLI
// (cmd/server/domain_directory_privacy_cmd.go). Extracting rule add/update/
// delete + scoped credential add/delete here keeps the web and headless paths
// from drifting: one validator matrix, one cross-rule containment check, one
// bcrypt-at-write, and one exactly-once converge-schedule per mutation
// (verify_wire_contract scar).
//
// Domain resolution and authorization stay in the adapters — HTTP scopes by
// tenant claims, the CLI is privileged. Every op below takes an
// already-authorized domainID, the same way egressops.SetPolicy takes a UserID.
package dirprivops

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ErrRuleNotFound is returned when a rule id does not exist or belongs to a
// different domain — the two collapse so a caller cannot probe rule existence
// across domains (HTTP maps both to 404 rule_not_found).
var ErrRuleNotFound = errors.New("directory-privacy rule not found on this domain")

// ErrCredentialNotFound is returned when a credential id does not exist or
// belongs to a different rule than the one named — the JAB-316 cross-rule
// containment guard, failing closed. HTTP maps both to 404 credential_not_found.
var ErrCredentialNotFound = errors.New("directory-privacy credential not found under this rule")

// ValidationError is a field-scoped input rejection. Msg is the bare,
// adapter-neutral message: the HTTP handler writes it straight to the wire
// (preserving the codes the endpoint has always returned) and the CLI decorates
// it with the offending flag name.
type ValidationError struct {
	Field string // path | realm | username | password
	Msg   string
}

func (e *ValidationError) Error() string { return e.Msg }

// Deps are the collaborators the ops operate on.
type Deps struct {
	Privacy repository.DomainDirectoryPrivacyRepository
	// Schedule converges the owning domain exactly once after a successful
	// mutation. HTTP passes the reconciler's in-process Schedule for an
	// immediate converge. The CLI passes nil by design: a reconciler built in
	// the short-lived CLI process without serve.go's full With* chain renders
	// the vhost with directory_privacy_rules omitted (reconciler.go: "dispatch
	// omits directory_privacy_rules → no htpasswd files written, no auth_basic
	// location blocks rendered"), so a CLI-side converge would strip the
	// protection it just created. The CLI relies on the daemon's next tick per
	// the established reconcile-apply pattern. nil is a no-op.
	Schedule func(domainID string)
	// BcryptCost overrides bcrypt.DefaultCost; 0 = default. Tests pass a low cost.
	BcryptCost int
}

func (d Deps) cost() int {
	if d.BcryptCost == 0 {
		return bcrypt.DefaultCost
	}
	return d.BcryptCost
}

func (d Deps) schedule(domainID string) {
	if d.Schedule != nil {
		d.Schedule(domainID)
	}
}

// --- validators (the single canonical copies) ---

var (
	// privacyPathRE requires at least one path character after the leading
	// slash; the p == "/" case is handled before the regex, so every path that
	// reaches it already has length >= 2.
	privacyPathRE     = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
	privacyUsernameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// ValidatePath canonicalizes and validates a protected-directory path. It
// rejects paths that are empty, too long, contain characters outside
// [A-Za-z0-9_./-], contain "//", or contain a ".." segment (traversal), and
// strips a trailing slash for consistent storage (the agent re-adds it when it
// emits `location ^~ <path>/`).
func ValidatePath(in string) (string, error) {
	p := strings.TrimSpace(in)
	if p == "" {
		return "", &ValidationError{Field: "path", Msg: "path required"}
	}
	if len(p) > 255 {
		return "", &ValidationError{Field: "path", Msg: "path too long (max 255)"}
	}
	if p == "/" {
		return "/", nil
	}
	if !privacyPathRE.MatchString(p) {
		return "", &ValidationError{Field: "path", Msg: "path must start with / and contain only [A-Za-z0-9_./-]"}
	}
	if strings.Contains(p, "//") {
		return "", &ValidationError{Field: "path", Msg: "path must not contain //"}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", &ValidationError{Field: "path", Msg: "path must not contain .."}
		}
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
	}
	return p, nil
}

// ValidateRealm canonicalizes and validates an auth realm. An empty realm
// defaults to "Restricted". The realm is emitted inside a double-quoted nginx
// `auth_basic "<realm>"` directive, so it must be printable ASCII and must not
// contain a double quote or backslash.
func ValidateRealm(in string) (string, error) {
	r := strings.TrimSpace(in)
	if r == "" {
		r = "Restricted"
	}
	if len(r) > 255 {
		return "", &ValidationError{Field: "realm", Msg: "realm too long (max 255)"}
	}
	for _, c := range r {
		if c < 0x20 || c > 0x7e {
			return "", &ValidationError{Field: "realm", Msg: "realm must be printable ASCII"}
		}
		if c == '"' || c == '\\' {
			return "", &ValidationError{Field: "realm", Msg: `realm must not contain " or \`}
		}
	}
	return r, nil
}

// ValidateUsername checks a basic-auth username against [A-Za-z0-9._-] (1-64).
func ValidateUsername(in string) error {
	if !privacyUsernameRE.MatchString(in) {
		return &ValidationError{Field: "username", Msg: "username must match [A-Za-z0-9._-] (1-64 chars)"}
	}
	return nil
}

// ValidatePassword checks a basic-auth password length (8-128).
func ValidatePassword(in string) error {
	if len(in) < 8 {
		return &ValidationError{Field: "password", Msg: "password must be at least 8 characters"}
	}
	if len(in) > 128 {
		return &ValidationError{Field: "password", Msg: "password too long (max 128)"}
	}
	return nil
}

// resolveRule loads a rule by id and confirms it belongs to domainID. Replaces
// the CLI's ListRulesByDomain + linear scan with an O(1) FindRuleByID plus the
// same containment check the HTTP adapter enforced; a missing rule or a rule on
// another domain both collapse to ErrRuleNotFound.
func resolveRule(ctx context.Context, deps Deps, domainID, ruleID string) (*models.DomainDirectoryPrivacyRule, error) {
	rule, err := deps.Privacy.FindRuleByID(ctx, ruleID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrRuleNotFound
		}
		return nil, err
	}
	if rule.DomainID != domainID {
		return nil, ErrRuleNotFound
	}
	return rule, nil
}

// --- rule ops ---

// CreateRule validates path + realm and creates a protected-directory rule,
// then schedules the owning domain once.
func CreateRule(ctx context.Context, deps Deps, domainID, path, realm string) (*models.DomainDirectoryPrivacyRule, error) {
	cleanPath, err := ValidatePath(path)
	if err != nil {
		return nil, err
	}
	cleanRealm, err := ValidateRealm(realm)
	if err != nil {
		return nil, err
	}
	row := &models.DomainDirectoryPrivacyRule{
		ID:       ids.NewULID(),
		DomainID: domainID,
		Path:     cleanPath,
		Realm:    cleanRealm,
	}
	if err := deps.Privacy.CreateRule(ctx, row); err != nil {
		return nil, err
	}
	deps.schedule(domainID)
	return row, nil
}

// UpdateRule resolves the rule (containment), validates the new realm, updates
// it, then schedules the owning domain once. Only the realm is mutable.
func UpdateRule(ctx context.Context, deps Deps, domainID, ruleID, realm string) (*models.DomainDirectoryPrivacyRule, error) {
	rule, err := resolveRule(ctx, deps, domainID, ruleID)
	if err != nil {
		return nil, err
	}
	cleanRealm, err := ValidateRealm(realm)
	if err != nil {
		return nil, err
	}
	if err := deps.Privacy.UpdateRule(ctx, rule.ID, cleanRealm); err != nil {
		return nil, err
	}
	rule.Realm = cleanRealm
	deps.schedule(domainID)
	return rule, nil
}

// DeleteRule resolves the rule (containment), deletes it (and its credentials,
// per the repository cascade), then schedules the owning domain once.
func DeleteRule(ctx context.Context, deps Deps, domainID, ruleID string) error {
	rule, err := resolveRule(ctx, deps, domainID, ruleID)
	if err != nil {
		return err
	}
	if err := deps.Privacy.DeleteRule(ctx, rule.ID); err != nil {
		return err
	}
	deps.schedule(domainID)
	return nil
}

// --- credential ops ---

// CreateCredential resolves the rule (containment), validates username +
// password, hashes the password at write, creates the credential, then
// schedules the owning domain once.
func CreateCredential(ctx context.Context, deps Deps, domainID, ruleID, username, password string) (*models.DomainDirectoryPrivacyCredential, error) {
	rule, err := resolveRule(ctx, deps, domainID, ruleID)
	if err != nil {
		return nil, err
	}
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(password, deps.cost())
	if err != nil {
		return nil, err
	}
	row := &models.DomainDirectoryPrivacyCredential{
		ID:           ids.NewULID(),
		RuleID:       rule.ID,
		Username:     username,
		PasswordHash: hash,
	}
	if err := deps.Privacy.CreateCredential(ctx, row); err != nil {
		return nil, err
	}
	deps.schedule(domainID)
	return row, nil
}

// DeleteCredential resolves the rule (containment), then loads the credential
// and confirms it belongs to THAT rule before deleting it — the JAB-316
// cross-rule credential-deletion guard, failing closed on a mismatch or a
// missing credential. Schedules the owning domain once on success.
func DeleteCredential(ctx context.Context, deps Deps, domainID, ruleID, credID string) error {
	rule, err := resolveRule(ctx, deps, domainID, ruleID)
	if err != nil {
		return err
	}
	cred, err := deps.Privacy.FindCredentialByID(ctx, credID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrCredentialNotFound
		}
		return err
	}
	if cred.RuleID != rule.ID {
		return ErrCredentialNotFound
	}
	if err := deps.Privacy.DeleteCredential(ctx, credID); err != nil {
		return err
	}
	deps.schedule(domainID)
	return nil
}
