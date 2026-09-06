// Content-selection for account backups (JAB-324).
//
// This is the second producer in backupmetadata, alongside the schema-v2
// metadata Build. Build answers "what state does this account carry"; SelectAll
// answers "what concrete database / mailbox / docker-app names must the agent
// be told to snapshot". Both run off the same Deps, so an adapter builds Deps
// once and the two producers can never drift on which repos they read.
//
// Before this module the selection logic was copied three times — the admin
// handler, the tenant self-shell handler, and the scheduler — and the copies
// diverged on error handling: the admin handler logged a lookup failure, the
// tenant handler swallowed it silently, the scheduler logged it. A silent
// swallow is a real backup bug: the agent SKIPS a stage whose list is empty
// (panel-agent backup_create.go), so a nil-on-error selection drops the whole
// category from the backup and records the stage as "skipped: none" as if the
// account owned nothing. SelectAll keeps whatever resolved and reports each
// failure as a structured Warning the caller logs the same way everywhere.
package backupmetadata

import (
	"context"
	"log/slog"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// selectionListLimit bounds each content-selection lookup. Matches the limit
// the three former per-adapter helpers used; an account with more than this
// many databases / domains / mailboxes is already far past any supported
// footprint.
const selectionListLimit = 10000

// Selection is the concrete content an account backup must include: the
// database / mailbox / docker-app names the agent is told to snapshot. The two
// database engines are kept apart (M37) so the agent dispatches the right dump
// tool per engine.
//
// An empty slice means "the account genuinely owns none of this". Because the
// agent skips a stage whose list is empty, a lookup FAILURE must never collapse
// into an empty slice — SelectAll keeps the partial result that resolved and
// reports the failure as a Warning instead.
type Selection struct {
	MariaDB    []string
	Postgres   []string
	Mailboxes  []string
	DockerApps []string
}

// WarningKind classifies a non-fatal content-selection lookup failure.
type WarningKind string

const (
	WarnDatabases        WarningKind = "databases"
	WarnDockerApps       WarningKind = "docker_apps"
	WarnServerDockerApps WarningKind = "server_docker_apps"
	WarnDomains          WarningKind = "domains"
	WarnMailboxes        WarningKind = "mailboxes"
)

// Warning is a structured record of a partial content-selection failure. It
// replaces the three adapters' divergent handling with one classification every
// caller logs the same way (LogWarnings). Scope is the id the failed lookup was
// scoped to: the user id, or the domain id for a per-domain mailbox failure.
type Warning struct {
	Kind  WarningKind
	Scope string
	Err   error
}

// SelectAll assembles the complete backup content selection for userID across
// every category (MariaDB + Postgres databases, mailboxes, docker-app slugs).
// isAdmin folds in the server-level docker apps (UserID NULL, GH #1360) that
// have no tenant account of their own.
//
// Every lookup failure is tolerated and reported as a Warning: the partial
// inventory that DID resolve is preserved (never silently shrunk to nil) and
// never escalated to a fatal error — a transient repo blip must not fail an
// otherwise-good scheduled backup. Callers log the warnings (LogWarnings) and
// proceed. A nil repo is legitimate wiring (a deployment without that surface)
// and yields no names and no warning, the same as an account that owns none.
func SelectAll(ctx context.Context, d Deps, userID string, isAdmin bool) (Selection, []Warning) {
	var sel Selection
	var warns []Warning

	// Databases: one lookup, split by engine. A blank engine is legacy
	// MariaDB. An unknown engine belongs to neither wire field and is
	// dropped from both — unchanged from the former per-engine helpers.
	if d.Databases != nil {
		rows, _, err := d.Databases.ListByUserID(ctx, userID, repository.ListOptions{Limit: selectionListLimit})
		if err != nil {
			warns = append(warns, Warning{Kind: WarnDatabases, Scope: userID, Err: err})
		} else {
			for _, r := range rows {
				switch r.Engine {
				case "postgres":
					sel.Postgres = append(sel.Postgres, r.Name)
				case "mariadb", "":
					sel.MariaDB = append(sel.MariaDB, r.Name)
				}
			}
		}
	}

	// Docker apps: the data trees live outside the home
	// (/var/lib/jabali/docker-apps/<EffectiveSlug>), so the agent must be
	// told the slugs explicitly or the backup omits the app (GH #954).
	if d.DockerApps != nil {
		rows, err := d.DockerApps.ListByUserID(ctx, userID)
		if err != nil {
			warns = append(warns, Warning{Kind: WarnDockerApps, Scope: userID, Err: err})
		} else {
			sel.DockerApps = append(sel.DockerApps, dockerSlugs(rows)...)
		}
		// GH #1360: an admin account also carries the server-level docker
		// apps (UserID NULL), whose only prior cover was a system backup.
		if isAdmin {
			extra, serr := serverLevelDockerSlugs(ctx, d.DockerApps)
			if serr != nil {
				warns = append(warns, Warning{Kind: WarnServerDockerApps, Scope: userID, Err: serr})
			} else {
				sel.DockerApps = append(sel.DockerApps, extra...)
			}
		}
	}

	// Mailboxes: domains → per-domain mailboxes. A per-domain failure is
	// tolerated (warn + skip) so one bad domain never hides the rest of the
	// account's mail.
	if d.Domains != nil && d.Mailboxes != nil {
		doms, _, err := d.Domains.ListByUserID(ctx, userID, repository.ListOptions{Limit: selectionListLimit})
		if err != nil {
			warns = append(warns, Warning{Kind: WarnDomains, Scope: userID, Err: err})
		} else {
			for _, dom := range doms {
				mbs, _, mErr := d.Mailboxes.ListByDomainID(ctx, dom.ID, repository.ListOptions{Limit: selectionListLimit})
				if mErr != nil {
					warns = append(warns, Warning{Kind: WarnMailboxes, Scope: dom.ID, Err: mErr})
					continue
				}
				for _, m := range mbs {
					sel.Mailboxes = append(sel.Mailboxes, m.EmailCached)
				}
			}
		}
	}

	return sel, warns
}

// dockerSlugs resolves each row to its EffectiveSlug (NOT Slug: a second
// instance of a catalog app carries an InstanceSlug that differs from the
// catalog slug, and the data tree lives at the effective slug). Nil rows and
// empty slugs are dropped — an empty slug would resolve the whole docker-apps
// root.
func dockerSlugs(rows []*models.DockerApp) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		if slug := r.EffectiveSlug(); slug != "" {
			out = append(out, slug)
		}
	}
	return out
}

// serverLevelDockerSlugs returns the EffectiveSlug of every live server-level
// docker app (models.DockerApp.UserID NULL, M48). Derived from ListAll + filter
// so the repo interface (and its mocks) stays put. Deleted tombstones and
// tenant-owned rows are excluded.
func serverLevelDockerSlugs(ctx context.Context, repo repository.DockerAppRepository) ([]string, error) {
	all, err := repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, r := range all {
		if r == nil || r.UserID != nil || r.Status == models.DockerAppStatusDeleted {
			continue
		}
		if slug := r.EffectiveSlug(); slug != "" {
			out = append(out, slug)
		}
	}
	return out, nil
}

// LogWarnings emits one log line per selection Warning through log. A nil logger
// is a no-op. This is the single place the adapters now share for surfacing a
// partial selection, replacing their divergent handling.
func LogWarnings(log *slog.Logger, warns []Warning) {
	if log == nil {
		return
	}
	for _, w := range warns {
		log.Warn("backup content selection partial: a lookup failed",
			"kind", string(w.Kind), "scope", w.Scope, "err", w.Err)
	}
}
