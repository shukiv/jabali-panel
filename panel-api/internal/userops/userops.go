// Package userops is the shared user-create logic (M41 ADR-0083
// follow-up to internal/dbops). REST handler at panel-api/internal/
// api/users.go and migrate-stage CreateUser orchestrator both call
// userops.Create so validation, kratos atomic, agent.user.create,
// and the panel-side row insert live in one place.
//
// Error model: typed Err* sentinels so HTTP can map to status codes
// (errors.Is(err, ErrUsernameTaken) → 409) and CLI / migration code
// can branch on stable kinds. Wrap with %w to preserve the chain.
package userops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// AgentCaller is the slice of agent functionality userops needs.
// One method — Call — keeps tests trivial.
type AgentCaller interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// Deps wires the collaborator repos + kratos client + agent. Repo
// + BcryptCost are required; Agent / Kratos / Packages are
// optional (callbacks gracefully skip when nil).
type Deps struct {
	Users      repository.UserRepository
	Packages   repository.PackageRepository
	Domains    repository.DomainRepository
	DockerApps repository.DockerAppRepository
	// DomainTeardowns persists the JAB-236 tombstones that make domain
	// deletion durable. Optional: nil keeps the pre-tombstone behaviour
	// (teardown attempted once, not retried).
	DomainTeardowns repository.DomainTeardownRepository
	Agent           AgentCaller
	KratosClient    *kratosclient.Client
	BcryptCost      int
	Log             *slog.Logger
}

// CreateInput is the shared input shape. Both callers (REST + the
// future migration CreateUser orchestrator) build this from their
// own argument parsing.
//
// SkipProvision matches the existing REST handler's flag: when
// true, the agent.user.create call is skipped (DB row + kratos
// identity only). Used by tests + dev paths.
type CreateInput struct {
	Email         string
	Password      string
	Username      *string // optional; derived from email when nil
	NameFirst     string
	NameLast      string
	IsAdmin       bool
	PackageID     *string
	SkipProvision bool
}

// CreateResult is the success envelope. ProvisionWarning is set
// when the panel row + kratos identity were created cleanly but
// agent.user.create failed best-effort — caller surfaces the
// warning to the operator without rolling back.
type CreateResult struct {
	User             *models.User
	ProvisionWarning string // populated only when agent.user.create soft-failed
}

// Sentinel errors — callers use errors.Is to map to HTTP status
// codes / CLI exit codes / migration-stage failure kinds.
var (
	ErrDeps            = errors.New("userops: dependencies not wired")
	ErrInvalidUsername = errors.New("userops: invalid username")
	ErrInvalidPackage  = errors.New("userops: invalid package id")
	ErrUsernameTaken   = errors.New("userops: username already exists")
	ErrEmailTaken      = errors.New("userops: email already exists")
	ErrKratosFailed    = errors.New("userops: kratos identity create failed")
	ErrInternal        = errors.New("userops: internal error")
)

var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Create produces a new panel user + (when wired) a Kratos identity
// + (when SkipProvision=false and not admin) an OS account.
// Pipeline matches the pre-extraction REST handler exactly:
//
//  1. validate username (effective username = req.Username when
//     set, else derived from email prefix)
//  2. validate package_id (when supplied)
//  3. bcrypt hash password
//  4. insert users row (rolled back on kratos failure)
//  5. kratos atomic CreateIdentityWithPassword (when client wired)
//  6. LinkKratosIdentity (with double-rollback on failure)
//  7. agent.user.create (best-effort; warning, not rollback)
//  8. malware-monitor reload (fire-and-forget goroutine)
//
// Returns ErrUsernameTaken / ErrEmailTaken on conflict; caller
// surfaces 409. ErrKratosFailed on kratos failure (panel row
// already rolled back). ErrInternal for unexpected failures.
func Create(ctx context.Context, d Deps, in CreateInput) (*CreateResult, error) {
	if d.Users == nil || d.BcryptCost == 0 {
		return nil, ErrDeps
	}
	// M54/GH#176: username is the login identifier; email is optional contact.
	// Require a password + a resolvable username (validated below). An empty
	// email is fine — the Kratos email trait is omitempty, so it's simply
	// omitted from the identity rather than sent as an invalid "".
	if in.Password == "" {
		return nil, fmt.Errorf("%w: password required", ErrInvalidUsername)
	}

	// Effective username — caller-supplied OR derived from email
	// prefix. Admins keep nil (no Linux user provisioned).
	var effectiveUsername *string
	if !in.IsAdmin {
		if in.Username != nil {
			effectiveUsername = in.Username
		} else {
			derived := UserFromEmail(in.Email)
			effectiveUsername = &derived
		}
		if effectiveUsername == nil || *effectiveUsername == "" || !usernameRe.MatchString(*effectiveUsername) {
			return nil, fmt.Errorf("%w: must match ^[a-z_][a-z0-9_-]{0,31}$", ErrInvalidUsername)
		}
	}

	// Package validation — when supplied + Packages wired.
	if in.PackageID != nil && *in.PackageID != "" && d.Packages != nil {
		if _, err := d.Packages.FindByID(ctx, *in.PackageID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrInvalidPackage
			}
			return nil, fmt.Errorf("%w: load package: %v", ErrInternal, err)
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), d.BcryptCost)
	if err != nil {
		return nil, fmt.Errorf("%w: bcrypt: %v", ErrInternal, err)
	}

	u := &models.User{
		ID:           ids.NewULID(),
		Email:        in.Email,
		Username:     effectiveUsername,
		NameFirst:    in.NameFirst,
		NameLast:     in.NameLast,
		PasswordHash: string(hash),
		IsAdmin:      in.IsAdmin,
		PackageID:    in.PackageID,
		// GH #316: new users default to webmail ON. The column default (1) only
		// applies when GORM omits the column, but a plain bool can't be omitted, so
		// set it explicitly or new users would insert webmail_enabled=false.
		WebmailEnabled: true,
	}
	if err := d.Users.Create(ctx, u); err != nil {
		return nil, mapInsertErr(err, effectiveUsername)
	}

	// Kratos atomic — when KratosClient wired. Rollback panel row
	// on failure so retries don't hit a username/email conflict.
	if d.KratosClient != nil {
		traits := kratosclient.AdminTraits{
			Email:   u.Email,
			IsAdmin: u.IsAdmin,
		}
		if u.Username != nil {
			traits.Username = *u.Username
		}
		identityID, kErr := d.KratosClient.CreateIdentityWithPassword(ctx, traits, u.PasswordHash)
		// ErrIdentityExisted = 409 conflict but we resolved the
		// existing identity id by email lookup. Reuse it instead of
		// rolling back the panel row — keeps migration reruns +
		// destroy-then-recreate cycles idempotent. Operator can rotate
		// the password via the panel afterward.
		if errors.Is(kErr, kratosclient.ErrIdentityExisted) && identityID != "" {
			if d.Log != nil {
				d.Log.Warn("kratos identity already exists; reusing",
					"user_id", u.ID, "email", u.Email, "kratos_id", identityID)
			}
			kErr = nil
		}
		if kErr != nil {
			if delErr := d.Users.Delete(ctx, u.ID); delErr != nil && d.Log != nil {
				d.Log.Error("kratos create failed AND panel rollback failed — orphan row",
					"user_id", u.ID, "email", u.Email,
					"kratos_err", kErr, "rollback_err", delErr)
			}
			return nil, fmt.Errorf("%w: %v", ErrKratosFailed, kErr)
		}
		u.KratosIdentityID = &identityID
		if linkErr := d.Users.LinkKratosIdentity(ctx, u.ID, identityID); linkErr != nil {
			// Undo both sides; best-effort.
			if delErr := d.KratosClient.DeleteIdentity(ctx, identityID); delErr != nil && d.Log != nil {
				d.Log.Error("panel link failed AND kratos rollback failed — orphan identity",
					"user_id", u.ID, "identity_id", identityID,
					"link_err", linkErr, "rollback_err", delErr)
			}
			if delErr := d.Users.Delete(ctx, u.ID); delErr != nil && d.Log != nil {
				d.Log.Error("panel link failed AND panel rollback failed — orphan row",
					"user_id", u.ID, "link_err", linkErr, "rollback_err", delErr)
			}
			return nil, fmt.Errorf("%w: link: %v", ErrInternal, linkErr)
		}
	}

	// Best-effort OS provisioning. Agent failure → warning, not
	// rollback (the user can SSH-into / FTP a re-provisioned
	// account later via the existing reprovision REST endpoint).
	res := &CreateResult{User: u}
	if d.Agent != nil && !in.SkipProvision && !in.IsAdmin && effectiveUsername != nil {
		raw, err := d.Agent.Call(ctx, "user.create", map[string]any{
			"username": *effectiveUsername,
			"home_dir": "/home/" + *effectiveUsername,
			"shell":    "/usr/local/bin/jabali-ssh-shell",
			"password": in.Password,
		})
		if err != nil {
			if d.Log != nil {
				d.Log.Warn("user agent provisioning failed",
					"user_id", u.ID, "email", u.Email, "err", err)
			}
			res.ProvisionWarning = "user saved but OS account provisioning failed: " + err.Error()
		} else {
			// JAB-33: user.create returns the provisioned OS UID. Persist it to
			// users.linux_uid so downstream (egress policy, quotas, SFTP) can map
			// the panel row to the Linux account. Previously the response was
			// discarded, leaving linux_uid NULL for every userops-created user
			// (admin UI + migration alike). Best-effort: a failed update is a
			// warning, not a rollback of the already-provisioned account.
			var pr struct {
				UID int `json:"uid"`
			}
			if jErr := json.Unmarshal(raw, &pr); jErr == nil && pr.UID > 0 {
				uid := uint32(pr.UID)
				u.LinuxUID = &uid
				if uErr := d.Users.Update(ctx, u); uErr != nil && d.Log != nil {
					d.Log.Warn("persist linux_uid failed", "user_id", u.ID, "uid", pr.UID, "err", uErr)
				}
			}
			// M33: re-evaluate maldet inotify watches now that a new
			// tenant home exists. Fire-and-forget; LMD inotify_minutes=45
			// covers missed reloads automatically. Use a fresh ctx
			// because the request ctx may already be cancelled by the
			// time the caller returns.
			if d.Agent != nil {
				go func(a AgentCaller) {
					bgCtx, bgCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer bgCancel()
					_, _ = a.Call(bgCtx, "security.malware.monitor.reload", map[string]any{})
				}(d.Agent)
			}
		}
	}
	return res, nil
}

// mapInsertErr translates a Users.Create error into the right
// sentinel. Username collision is the common case + caller wants
// 409 not 500 for it.
func mapInsertErr(err error, effective *string) error {
	if err == nil {
		return nil
	}
	// Repository abstraction layer returns a typed sentinel for any
	// unique-index collision. Treat as username/email collision based
	// on whether an effective username was derived for this create.
	if errors.Is(err, repository.ErrConflict) {
		if effective != nil {
			return fmt.Errorf("%w: %v", ErrUsernameTaken, *effective)
		}
		return ErrEmailTaken
	}
	s := err.Error()
	if strings.Contains(s, "Duplicate entry") || strings.Contains(s, "1062") {
		// Mariadb duplicate-key. Could be username OR email — the
		// error string contains the index name; cheap substring
		// match resolves which.
		switch {
		case strings.Contains(s, "username"):
			return fmt.Errorf("%w: %v", ErrUsernameTaken, *effective)
		case strings.Contains(s, "email"):
			return ErrEmailTaken
		default:
			// Fall back to ErrUsernameTaken when we can tell from
			// the effective username path; else generic conflict.
			if effective != nil {
				return fmt.Errorf("%w: %v", ErrUsernameTaken, *effective)
			}
			return ErrEmailTaken
		}
	}
	return fmt.Errorf("%w: %v", ErrInternal, err)
}

// UserFromEmail derives a Linux username from an email. Takes the
// part before '@'. Callers validate downstream (usernameRe). Same
// shape as the legacy linuxUserFromEmail in internal/api.
func UserFromEmail(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return ""
}
