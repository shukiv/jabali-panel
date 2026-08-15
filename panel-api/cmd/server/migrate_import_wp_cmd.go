package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newMigrateImportWPCmd (GH #647 S4) imports a STAGED wordpress_ssh job into a
// destination: provisions a Jabali DB+user, restores the dump, extracts the file
// tarball, moves it into the docroot (agent, containment), and rewrites
// wp-config.php to the new credentials. Operator-gated (A3) — run after
// `jabali migrate pull-source` has staged dump.sql + files.tar.gz.
func newMigrateImportWPCmd() *cobra.Command {
	var jobID, destUser, destDomain string
	cmd := &cobra.Command{
		Use:     "import-wp",
		Short:   "Import a staged wordpress_ssh migration into a destination (GH #647)",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobID == "" || destUser == "" || destDomain == "" {
				return errors.New("--job-id, --dest-user, --dest-domain are required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Minute)
			defer cancel()
			jobs := repository.NewMigrationJobRepository(sharedDB)
			job, err := jobs.FindByID(ctx, jobID)
			if err != nil {
				return fmt.Errorf("load job: %w", err)
			}
			// GH #666: admin-created jobs have no target_user_id — resolve it
			// from --dest-user (the OS username) so the CLI import isn't a dead end.
			tuid := ""
			if job.TargetUserID != nil && *job.TargetUserID != "" {
				tuid = *job.TargetUserID
			} else {
				u, uerr := repository.NewUserRepository(sharedDB).FindByUsername(ctx, destUser)
				if uerr != nil || u == nil {
					return fmt.Errorf("job has no target_user_id and --dest-user %q not found: %w", destUser, uerr)
				}
				tuid = u.ID
			}
			out := cmd.OutOrStdout()
			return importWordPressSSH(ctx, out, jobs, job, destUser, tuid, destDomain)
		},
	}
	cmd.Flags().StringVar(&jobID, "job-id", "", "wordpress_ssh migration_jobs.id (staged)")
	cmd.Flags().StringVar(&destUser, "dest-user", "", "destination OS username (owns the docroot)")
	cmd.Flags().StringVar(&destDomain, "dest-domain", "", "destination domain (docroot = /home/<user>/domains/<domain>/public_html)")
	return cmd
}

func importWordPressSSH(ctx context.Context, out io.Writer,
	jobs repository.MigrationJobRepository, job *models.MigrationJob,
	destUser, targetUserID, destDomain string) error {

	pf := func(format string, a ...any) { fmt.Fprintf(out, format, a...) }
	_ = jobs.UpdateState(ctx, job.ID, models.MigrationStateRestoring, nil) // import phase
	stRestore := wpMigStartStage(ctx, jobs, job.ID, "restore")
	stageDir := filepath.Join("/var/lib/jabali-migrations", job.ID)
	dumpSQL := filepath.Join(stageDir, "dump.sql")
	filesTar := filepath.Join(stageDir, "files.tar.gz")
	filesDir := filepath.Join(stageDir, "files")
	docSubpath := filepath.Join("domains", destDomain, "public_html")
	docroot := filepath.Join("/home", destUser, docSubpath)

	// Application-install row so the migration shows in the Applications table
	// with live status (installing -> ready/failed), like a fresh install.
	appRepo := repository.NewApplicationInstallRepository(sharedDB)
	installID := ""
	if dom, derr := repository.NewDomainRepository(sharedDB).FindByName(ctx, destDomain); derr == nil && dom != nil {
		email := "migrated@" + destDomain
		if u, uerr := repository.NewUserRepository(sharedDB).FindByID(ctx, targetUserID); uerr == nil && u.Email != "" {
			email = u.Email
		}
		// Reuse the row the pull created (status=installing); else create it.
		if existing, _ := appRepo.FindByDomainAndSubdirectoryAndAppType(ctx, dom.ID, "", "wordpress"); existing != nil {
			installID = existing.ID
			_ = appRepo.UpdateStatus(ctx, installID, "installing", nil, nil)
		} else {
			row := &models.ApplicationInstall{
				ID: ids.NewULID(), UserID: targetUserID, DomainID: dom.ID,
				AppType: "wordpress", Subdirectory: "", Status: "installing",
				AdminUsername: "admin", AdminEmail: email, Locale: "en_US",
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := appRepo.Create(ctx, row); err == nil {
				installID = row.ID
			}
		}
	}
	markInstall := func(status string, lastErr *string, version *string) {
		if installID != "" {
			_ = appRepo.UpdateStatus(ctx, installID, status, lastErr, version)
		}
	}

	fail := func(reason error) error {
		msg := "import-wp: " + reason.Error()
		_ = jobs.UpdateState(ctx, job.ID, models.MigrationStateFailed, &msg)
		_ = jobs.UpdateStage(ctx, stRestore, "failed", 0, &msg)
		markInstall("failed", &msg, nil)
		return reason
	}

	// --- 1. provision DB + user + grant, restore the dump (agent, root) ---
	dbName := wpMigDBName(destUser)
	pwd, err := randHex(16)
	if err != nil {
		return fail(err)
	}
	pf("  → creating DB %s ...\n", dbName)
	if _, err := sharedAgent.Call(ctx, "db.create", map[string]any{
		"db_name": dbName, "charset": "utf8mb4", "collation": "utf8mb4_unicode_ci",
	}); err != nil {
		return fail(fmt.Errorf("db.create: %w", err))
	}
	if _, err := sharedAgent.Call(ctx, "db_user.create", map[string]any{
		"db_user_name": dbName, "password": pwd,
	}); err != nil {
		return fail(fmt.Errorf("db_user.create: %w", err))
	}
	if _, err := sharedAgent.Call(ctx, "db_user.grant", map[string]any{
		"db_name": dbName, "db_user_name": dbName, "privileges": []string{"ALL"},
	}); err != nil {
		return fail(fmt.Errorf("db_user.grant: %w", err))
	}
	pf("  → restoring dump into %s ...\n", dbName)
	// db.restore unlinks its path — restore from a COPY so the staged dump
	// survives for a retry (no re-pull needed).
	restoreCopy := dumpSQL + ".restoring"
	if err := copyFile(dumpSQL, restoreCopy); err != nil {
		return fail(fmt.Errorf("stage dump copy: %w", err))
	}
	if _, err := sharedAgent.Call(ctx, "db.restore", map[string]any{
		"db_name": dbName, "path": restoreCopy, "reset_before_restore": true,
	}); err != nil {
		_ = os.Remove(restoreCopy)
		return fail(fmt.Errorf("db.restore: %w", err))
	}
	// Panel rows so the DB shows in the UI (best-effort — the site works regardless).
	dbRow := &models.Database{
		ID: ids.NewULID(), UserID: targetUserID, Name: dbName,
		Engine: "mariadb", Charset: "utf8mb4", Collation: "utf8mb4_unicode_ci",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_ = repository.NewDatabaseRepository(sharedDB).Create(ctx, dbRow)
	// GH #670: link the migrated app to the DB it created.
	if installID != "" {
		_ = sharedDB.WithContext(ctx).Model(&models.ApplicationInstall{}).
			Where("id = ?", installID).Update("db_id", dbRow.ID).Error
	}

	// --- 2. extract the file tarball to staging (containment-safe) ---
	pf("  → extracting files ...\n")
	// JAB-241: same disk preflight every other extract call site runs —
	// this path was the one entry point that skipped it.
	if err := migrate.CheckExtractDiskSpace(filesTar, filesDir); err != nil {
		return err
	}
	if err := migrate.ExtractTarGz(filesTar, filesDir); err != nil {
		return fail(fmt.Errorf("extract: %w", err))
	}

	// --- 3. move files -> docroot (agent, as the dest user, containment) ---
	pf("  → moving files into %s ...\n", docroot)
	if _, err := sharedAgent.Call(ctx, "migration.import_home", map[string]any{
		"job_id": job.ID, "src_dir": filesDir, "dest_user": destUser, "dest_subpath": docSubpath,
	}); err != nil {
		return fail(fmt.Errorf("import_home: %w", err))
	}

	// GH #667: nginx serves index.html before index.php — move the Jabali
	// placeholder aside so the migrated WordPress front controller wins at "/".
	if _, mvErr := sharedAgent.Call(ctx, "files.move", map[string]any{
		"user_id": targetUserID, "username": destUser,
		"old_path": filepath.Join(docroot, "index.html"),
		"new_path": filepath.Join(docroot, "index.html.pre-migration-bak"),
	}); mvErr != nil {
		pf("  (note: placeholder index.html not moved: %v)\n", mvErr)
	}

	// --- 4. rewrite wp-config.php to the new Jabali DB creds ---
	pf("  → rewriting wp-config.php ...\n")
	wpConfig := filepath.Join(docroot, "wp-config.php")
	raw, err := sharedAgent.Call(ctx, "files.read", map[string]any{
		"user_id": targetUserID, "username": destUser, "path": wpConfig,
	})
	if err != nil {
		return fail(fmt.Errorf("files.read wp-config: %w", err))
	}
	content, _ := agentFileContent(raw)
	// DB_HOST localhost (MariaDB via the default socket); user = db name (Jabali convention).
	updated, changed := migrate.RewriteWPConfigDB(content, dbName, dbName, pwd, "localhost")
	// GH #621: drop the SOURCE's Jabali cache constants so the migrated site
	// doesn't read/write the source tenant's Redis namespace (cross-tenant
	// bleed). The panel re-stamps fresh per-tenant constants when cache is enabled.
	if stripped := migrate.StripJabaliCacheBlock(updated); stripped != updated {
		updated = stripped
		pf("  -> reset cache config (source constants stripped; cache starts COLD — enable + auto-warm in the panel)\n")
	}
	if !changed {
		pf("  (warning: wp-config.php DB constants unchanged — check format)\n")
	}
	if _, err := sharedAgent.Call(ctx, "files.write", map[string]any{
		"user_id": targetUserID, "username": destUser, "path": wpConfig,
		"content": updated, "mode": "overwrite",
	}); err != nil {
		return fail(fmt.Errorf("files.write wp-config: %w", err))
	}

	// --- 5. domain-change: serialized-safe search-replace (old siteurl -> new) ---
	oldURL := ""
	if job.ManifestJSON != nil {
		var facts struct {
			SiteURL string `json:"siteurl"`
		}
		_ = json.Unmarshal([]byte(*job.ManifestJSON), &facts)
		oldURL = facts.SiteURL
	}
	newURL := "https://" + destDomain
	if oldURL != "" && oldURL != newURL {
		pf("  → search-replace %s -> %s (serialized-safe) ...\n", oldURL, newURL)
		if _, err := sharedAgent.Call(ctx, "wordpress.search_replace", map[string]any{
			"os_user": destUser, "path": docroot, "old_url": oldURL, "new_url": newURL,
		}); err != nil {
			return fail(fmt.Errorf("search-replace: %w", err))
		}
	} else {
		pf("  (same domain — no search-replace)\n")
	}

	wpMigDoneStage(ctx, jobs, stRestore)
	_ = jobs.UpdateState(ctx, job.ID, models.MigrationStateDone, nil)
	var wpVer *string
	if job.ManifestJSON != nil {
		var f struct {
			WPVersion string `json:"wp_version"`
		}
		if json.Unmarshal([]byte(*job.ManifestJSON), &f) == nil && f.WPVersion != "" {
			wpVer = &f.WPVersion
		}
	}
	markInstall("ready", nil, wpVer)
	// GH #669: reap the bearer-token secret + the large staging dir now that the
	// migration is done (retry no longer needs them).
	_ = os.RemoveAll(stageDir)
	_ = os.Remove(filepath.Join(migrate.SecretsDir, job.ID+".env"))
	pf("  \u2713 imported into %s (DB %s).\n", docroot, dbName)
	return nil
}

// wpMigDBName builds a valid Jabali DB name (^[a-z][a-z0-9_]{1,29}$) from the OS
// user + a short random suffix.
func wpMigDBName(osUser string) string {
	base := ""
	for _, r := range osUser {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			base += string(r)
		}
		if len(base) >= 12 {
			break
		}
	}
	if base == "" || (base[0] >= '0' && base[0] <= '9') {
		base = "wp" + base
	}
	suf, _ := randHex(3)
	return base + "_wpm" + suf
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// agentFileContent unmarshals a files.read agent response {content, is_binary}.
func agentFileContent(raw json.RawMessage) (string, bool) {
	var r struct {
		Content  string `json:"content"`
		IsBinary bool   `json:"is_binary"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.IsBinary {
		return "", false
	}
	return r.Content, true
}

// copyFile copies src to dst (0640). Used to preserve the staged dump across a
// db.restore that unlinks its input.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
