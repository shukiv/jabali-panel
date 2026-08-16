package userops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// SuspendResult / UnsuspendResult carry the domain count + best-effort,
// step-named warnings so the REST handler maps them 1:1 to its response keys
// (kratos_warning / domain_warning / os_warning) and the CLI can print them.
type SuspendResult struct {
	AlreadySuspended bool
	DomainsDisabled  int64
	KratosWarning    string
	DomainWarning    string
	OSWarning        string
}

type UnsuspendResult struct {
	AlreadyActive  bool
	DomainsEnabled int64
	KratosWarning  string
	DomainWarning  string
	OSWarning      string
}

// kratosStateWarning applies a Kratos identity state change (+ invalidate on
// suspend) and returns a warning string, "" on success. Shared by both paths.
func kratosStateWarning(ctx context.Context, d Deps, identityID, state string, invalidate bool) string {
	if d.KratosClient == nil {
		return "kratos_client_unavailable"
	}
	warn := ""
	if err := d.KratosClient.SetIdentityState(ctx, identityID, state); err != nil {
		if errors.Is(err, kratosclient.ErrIdentityNotFound) {
			warn = "kratos_identity_not_found"
		} else {
			warn = "kratos_state_patch_failed: " + err.Error()
		}
	}
	if invalidate {
		d.KratosClient.InvalidateIdentity(identityID)
	}
	return warn
}

// Suspend applies the full suspend cascade shared by the admin REST handler
// (POST /admin/users/:id/suspend) and the `jabali user suspend` CLI (JAB-127):
//
//  1. users.suspended = 1 (+ reason) — the load-bearing login gate.
//  2. Kratos identity -> inactive + invalidate cached whoami (best-effort).
//  3. disable every owned domain.
//  4. stop the tenant's Docker apps (best-effort).
//  5. agent user.suspend — OS: drop from jabali-sftp group + lock password.
//
// Refusing to suspend an admin is the CALLER's responsibility. Steps 2-5 are
// best-effort: a failure becomes a warning, never a hard error, because step 1
// already denies access.
func Suspend(ctx context.Context, d Deps, user *models.User, reason string) (SuspendResult, error) {
	res := SuspendResult{}
	if d.Users == nil || user == nil {
		return res, errors.New("userops.Suspend: users repo + user required")
	}
	if user.Suspended {
		res.AlreadySuspended = true
		return res, nil
	}

	if err := d.Users.SetSuspended(ctx, user.ID, true, reason); err != nil {
		return res, fmt.Errorf("suspend db write: %w", err)
	}

	if user.KratosIdentityID != nil && *user.KratosIdentityID != "" {
		res.KratosWarning = kratosStateWarning(ctx, d, *user.KratosIdentityID, "inactive", true)
	}

	if d.Domains != nil {
		if n, err := d.Domains.BulkSetEnabledByUserID(ctx, user.ID, false); err != nil {
			res.DomainWarning = "domain_bulk_disable_failed: " + err.Error()
		} else {
			res.DomainsDisabled = n
		}
	}

	if d.DockerApps != nil && d.Agent != nil {
		if apps, aerr := d.DockerApps.ListByUserID(ctx, user.ID); aerr == nil {
			for _, app := range apps {
				actx, acancel := context.WithTimeout(ctx, 2*time.Minute)
				if _, serr := d.Agent.Call(actx, "docker_app.stop", map[string]any{"slug": app.EffectiveSlug()}); serr != nil {
					if d.Log != nil {
						d.Log.Warn("suspend: docker_app.stop failed", "user_id", user.ID, "slug", app.EffectiveSlug(), "err", serr)
					}
				} else {
					_ = d.DockerApps.UpdateStatus(actx, app.ID, models.DockerAppStatusStopped, nil)
				}
				acancel()
			}
		} else if d.Log != nil {
			d.Log.Warn("suspend: list user docker apps failed", "user_id", user.ID, "err", aerr)
		}
	}

	if user.Username != nil && *user.Username != "" && d.Agent != nil {
		if _, err := d.Agent.Call(ctx, "user.suspend", map[string]any{"username": *user.Username}); err != nil {
			res.OSWarning = "user_os_suspend_failed: " + err.Error()
		}
		// JAB-254: lock every FTP/SFTP subaccount alias immediately so a
		// suspended owner's delegated credentials stop working at once,
		// not on the next reconcile tick. The reconciler's owner-
		// eligibility gate is the durable backstop and also handles
		// restore-to-desired on unsuspension, so this is best-effort.
		if _, err := d.Agent.Call(ctx, "ftpaccount.lock_tenant", map[string]any{"tenant_username": *user.Username}); err != nil {
			if res.OSWarning == "" {
				res.OSWarning = "ftp_alias_lock_failed: " + err.Error()
			}
		}
	}

	return res, nil
}

// Unsuspend reverses the cascade: flag off, Kratos active, domains re-enabled,
// OS user unlocked. It does NOT restart Docker apps (matches the REST handler —
// the operator restarts them deliberately). Best-effort on steps 2-4.
func Unsuspend(ctx context.Context, d Deps, user *models.User) (UnsuspendResult, error) {
	res := UnsuspendResult{}
	if d.Users == nil || user == nil {
		return res, errors.New("userops.Unsuspend: users repo + user required")
	}
	if !user.Suspended {
		res.AlreadyActive = true
		return res, nil
	}

	if err := d.Users.SetSuspended(ctx, user.ID, false, ""); err != nil {
		return res, fmt.Errorf("unsuspend db write: %w", err)
	}

	if user.KratosIdentityID != nil && *user.KratosIdentityID != "" {
		res.KratosWarning = kratosStateWarning(ctx, d, *user.KratosIdentityID, "active", false)
	}

	if d.Domains != nil {
		if n, err := d.Domains.BulkSetEnabledByUserID(ctx, user.ID, true); err != nil {
			res.DomainWarning = "domain_bulk_enable_failed: " + err.Error()
		} else {
			res.DomainsEnabled = n
		}
	}

	if user.Username != nil && *user.Username != "" && d.Agent != nil {
		if _, err := d.Agent.Call(ctx, "user.unsuspend", map[string]any{"username": *user.Username}); err != nil {
			res.OSWarning = "user_os_unsuspend_failed: " + err.Error()
		}
	}

	return res, nil
}
