// Account-restore extension for system.restore. When the caller passes
// include_accounts=true, after the system stages land we walk every
// kind=account_backup,stage=manifest snapshot in the same repo, group
// by user-id, pick the newest manifest per user, materialize each
// stage to /var/lib/jabali-backups/restore-staging/<job>/accounts/<uid>/,
// and (when apply=true) rsync home onto /home/<username> + load each
// per-user database SQL via the unix socket.
//
// The reconstructAccountState helper also re-populates jabali_panel
// rows from the schema-v2 metadata bundle the source emitted at
// backup-time. Disaster recovery thus works even when the source's
// system_backup panel_db dump is stale (user was deleted between
// account-backup and system-backup, or the restore target is a
// different host with no rows at all).
package commands

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/json"
	"fmt"
	mathRand "math/rand"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"github.com/oklog/ulid/v2"
)

// cryptoRandRead aliases crypto/rand.Read so newUUIDv4 can swap in a
// fake in unit tests without dragging the import alias into every call
// site (and so the failure path stays grep-able).
var cryptoRandRead = cryptoRand.Read

var (
	ulidEntropyMu sync.Mutex
	ulidEntropy   = ulid.Monotonic(mathRand.New(mathRand.NewSource(time.Now().UnixNano())), 0)
)

// newRestoreULID returns a fresh ULID for synthesised panel rows.
// Distinct name from the package's existing newULID() (which has a
// (string, error) signature in security_malware.go).
func newRestoreULID() string {
	ulidEntropyMu.Lock()
	defer ulidEntropyMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), ulidEntropy).String()
}

// reconstructionDomainNameRE constrains domain name characters so the
// /home/<u>/domains/* dir scan can't smuggle shell metacharacters into
// our INSERT. Mirrors panel-api's create-time validation.
var reconstructionDomainNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// accountRestorePlan groups the newest manifest snapshot per user-id.
type accountRestorePlan struct {
	UserID         string
	ManifestSnapID string
	ManifestTime   time.Time
}

// discoverAccountManifests queries the repo for every
// kind=account_backup, stage=manifest snapshot and returns the newest
// per user-id (so a 5-day backup chain collapses to one restore
// target per user).
func discoverAccountManifests(ctx context.Context, c *backup.Client) ([]accountRestorePlan, error) {
	snaps, err := c.Snapshots(ctx, []backup.Tag{
		backup.MakeTag(backup.TagKeyKind, backup.KindAccountBackup),
		backup.MakeTag(backup.TagKeyStage, backup.StageManifest),
	})
	if err != nil {
		return nil, err
	}
	byUser := map[string]accountRestorePlan{}
	for _, s := range snaps {
		// restic --tag is OR semantics across multiple flags, so the
		// returned set may include foreign tags too. We filter to the
		// expected pair here defensively.
		hasKind, hasStage := false, false
		userID := ""
		for _, t := range s.Tags {
			switch {
			case t == "kind=account_backup":
				hasKind = true
			case t == "stage=manifest":
				hasStage = true
			case strings.HasPrefix(t, "user-id="):
				userID = strings.TrimPrefix(t, "user-id=")
			}
		}
		if !hasKind || !hasStage || userID == "" {
			continue
		}
		if cur, ok := byUser[userID]; !ok || s.Time.After(cur.ManifestTime) {
			byUser[userID] = accountRestorePlan{
				UserID: userID, ManifestSnapID: s.ID, ManifestTime: s.Time,
			}
		}
	}
	plans := make([]accountRestorePlan, 0, len(byUser))
	for _, p := range byUser {
		plans = append(plans, p)
	}
	return plans, nil
}

// restoreAccounts is the entry point called from systemRestoreHandler
// when include_accounts=true. Returns operator-visible applied/warning
// lines that the handler appends to its own out.Applied / out.ApplyWarnings.
func restoreAccounts(ctx context.Context, c *backup.Client, jobID, stagingRoot string, apply bool) ([]string, []string) {
	var applied, warnings []string
	plans, err := discoverAccountManifests(ctx, c)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("discover account manifests: %v", err))
		return applied, warnings
	}
	if len(plans) == 0 {
		warnings = append(warnings, "no account_backup manifest snapshots found in repo")
		return applied, warnings
	}

	for _, plan := range plans {
		body, derr := c.Dump(ctx, plan.ManifestSnapID, "/manifest.json")
		if derr != nil {
			warnings = append(warnings,
				fmt.Sprintf("user-id=%s: read manifest: %v", plan.UserID, derr))
			continue
		}
		manifest, perr := backup.AccountManifestFromBytes(stripTar(body))
		if perr != nil {
			warnings = append(warnings,
				fmt.Sprintf("user-id=%s: parse manifest: %v", plan.UserID, perr))
			continue
		}

		userStaging := filepath.Join(stagingRoot, "accounts", plan.UserID)
		if err := os.MkdirAll(userStaging, 0o750); err != nil {
			warnings = append(warnings,
				fmt.Sprintf("user-id=%s: mkdir staging: %v", plan.UserID, err))
			continue
		}

		stagedStages := 0
		for _, st := range manifest.Stages {
			if st.SnapshotID == "" || st.Status != backup.StageStatusOK {
				continue
			}
			tgt := filepath.Join(userStaging, st.Name)
			if err := os.MkdirAll(tgt, 0o750); err != nil {
				warnings = append(warnings,
					fmt.Sprintf("user-id=%s stage=%s: mkdir: %v", plan.UserID, st.Name, err))
				continue
			}
			if err := c.Restore(ctx, backup.RestoreOpts{SnapshotID: st.SnapshotID, Target: tgt}); err != nil {
				warnings = append(warnings,
					fmt.Sprintf("user-id=%s stage=%s: restic restore: %v", plan.UserID, st.Name, err))
				continue
			}
			stagedStages++
		}
		applied = append(applied,
			fmt.Sprintf("staged: user=%s id=%s stages=%d", manifest.User.Username, plan.UserID, stagedStages))

		if !apply {
			continue
		}

		username := manifest.User.Username
		if !backupUsernameRE.MatchString(username) {
			warnings = append(warnings,
				fmt.Sprintf("user-id=%s: invalid username %q in manifest, skipping apply", plan.UserID, username))
			continue
		}

		if err := ensureLinuxUser(ctx, username); err != nil {
			warnings = append(warnings, fmt.Sprintf("user=%s useradd: %v", username, err))
			continue
		}

		homeSrc := filepath.Join(userStaging, "home", "home", username)
		if _, err := os.Stat(homeSrc); err == nil {
			if err := rsyncOnto(ctx, homeSrc+"/", "/home/"+username+"/"); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s home rsync: %v", username, err))
			} else {
				// Symlink-safe recursive chown (Gitea #498): the restored
				// home tree is snapshot-controlled, so Walk+Lchown instead of
				// `chown -R` (which follows symlinks out of /home/<user>).
				if u, lerr := user.Lookup(username); lerr != nil {
					warnings = append(warnings, fmt.Sprintf("user=%s home chown lookup: %v", username, lerr))
				} else {
					uid, _ := strconv.Atoi(u.Uid)
					gid, _ := strconv.Atoi(u.Gid)
					if cerr := chownTreeRecursive("/home/"+username, uid, gid); cerr != nil {
						warnings = append(warnings, fmt.Sprintf("user=%s home chown: %v", username, cerr))
					}
				}
				applied = append(applied, fmt.Sprintf("home: %s -> /home/%s", username, username))
			}
		}

		// MariaDB schema + data restore for each db stage in the manifest.
		for _, st := range manifest.Stages {
			if st.Name != backup.StageDB || st.Status != backup.StageStatusOK {
				continue
			}
			if len(st.Items) == 0 {
				continue
			}
			db := st.Items[0]
			if !dbNameRE.MatchString(db) {
				warnings = append(warnings, fmt.Sprintf("user=%s db=%s: invalid name", username, db))
				continue
			}
			sqlPath := filepath.Join(userStaging, "db", db+".sql")
			if _, err := os.Stat(sqlPath); err != nil {
				warnings = append(warnings,
					fmt.Sprintf("user=%s db=%s: SQL missing at %s", username, db, sqlPath))
				continue
			}
			if err := loadAccountDB(ctx, db, sqlPath); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s db=%s load: %v", username, db, err))
				continue
			}
			applied = append(applied, fmt.Sprintf("db: %s.%s", username, db))
		}

		// Read schema-v2 metadata bundle (panel rows). Older v1 bundles
		// can still exist in the wild; the parser tolerates missing
		// fields so the output is just less complete.
		meta := readMetaBundle(userStaging)

		// Reconstruct user + Kratos identity rows first (every other
		// FK target hangs off them).
		stateApplied, stateWarnings := reconstructAccountState(ctx, manifest, meta, username)
		applied = append(applied, stateApplied...)
		warnings = append(warnings, stateWarnings...)
	}
	return applied, warnings
}

// ensureLinuxUser is idempotent. `id <name>` succeeds when the user
// exists; otherwise we useradd -m -s /bin/bash. Group/shell
// canonicalization (jabali-* membership, jabali-sftp, slice file) is
// the panel reconciler's job — runs after the panel comes back up
// and reads jabali_panel.users.
func ensureLinuxUser(ctx context.Context, username string) error {
	if err := execCommandContext(ctx, "id", username).Run(); err == nil {
		return nil
	}
	out, err := execCommandContext(ctx, "useradd", "-m", "-s", "/bin/bash", username).CombinedOutput()
	if err != nil {
		return fmt.Errorf("useradd %s: %w (output: %s)",
			username, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// rsyncOnto wraps rsync -a --delete-after for the simple
// staged-tree-onto-target case. Trailing slash on src means "copy
// contents into target", matching how home stages were captured.
func rsyncOnto(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	out, err := execCommandContext(ctx, "rsync", "-a", "--delete-after", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rsync %s -> %s: %w (output: %s)",
			src, dst, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// loadAccountDB CREATEs the database (if missing) then pipes the SQL
// dump through the mariadb client over the unix socket.
func loadAccountDB(ctx context.Context, db, sqlPath string) error {
	create := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` "+
		"DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;", db)
	if out, err := execCommandContext(ctx, "mariadb",
		"--protocol=socket", "--socket=/run/mysqld/mysqld.sock",
		"-e", create,
	).CombinedOutput(); err != nil {
		return fmt.Errorf("create db %s: %w (output: %s)",
			db, err, strings.TrimSpace(string(out)))
	}
	f, err := os.Open(sqlPath) // #nosec G304 — path built from server-controlled stage dir
	if err != nil {
		return fmt.Errorf("open %s: %w", sqlPath, err)
	}
	defer f.Close()
	cmd := execCommandContext(ctx, "mariadb",
		"--protocol=socket", "--socket=/run/mysqld/mysqld.sock",
		db,
	)
	cmd.Stdin = f
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("load %s: %w (output: %s)",
			db, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// readMetaBundle parses the AccountMetadata blob staged at
// <userStaging>/meta/metadata.json. Returns nil silently when the meta
// stage was skipped at backup time.
func readMetaBundle(userStaging string) *backup.AccountMetadata {
	candidates := []string{
		filepath.Join(userStaging, "meta", "metadata.json"),
		filepath.Join(userStaging, "meta", "stdin"),
	}
	for _, p := range candidates {
		body, err := os.ReadFile(p) //nolint:gosec
		if err != nil {
			continue
		}
		var m backup.AccountMetadata
		if err := json.Unmarshal(body, &m); err != nil {
			continue
		}
		return &m
	}
	return nil
}

// runMariaDBStmt fires one --execute= statement over the unix socket
// and returns the combined stdout+stderr when it fails. Used by every
// reconstruction INSERT below.
func runMariaDBStmt(ctx context.Context, stmt string) error {
	cmd := execCommandContext(ctx, "mariadb",
		"--protocol=socket", "--socket=/run/mysqld/mysqld.sock",
		"-e", stmt,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// reconstructAccountState fills in jabali_panel + jabali_kratos rows
// for one user from the schema-v2 metadata bundle. Idempotent
// (every INSERT is INSERT IGNORE) so re-running a restore only fills
// gaps, never overwrites existing rows.
//
// FK dependency order is honoured throughout:
//   users → kratos identity → php_pools → domains → ssl → mailboxes →
//   forwarders/autoresponders/shares → databases → database_users →
//   grants → app_installs → ssh_keys/cron_jobs/dnssec/egress/limits
func reconstructAccountState(ctx context.Context, manifest *backup.AccountManifest,
	meta *backup.AccountMetadata, username string) ([]string, []string) {
	var applied, warnings []string
	userID := manifest.User.ID

	// 1. users + kratos identity (must precede every FK target). Use
	//    schema-v2 MetadataUser/Kratos when available; fall back to
	//    manifest.User for old v1 bundles.
	var userRow *backup.MetadataUser
	var kratos *backup.MetadataKratos
	if meta != nil && meta.User.ID != "" {
		userRow = &meta.User
		kratos = meta.Kratos
	} else {
		userRow = &backup.MetadataUser{
			ID: manifest.User.ID, Email: manifest.User.Email,
			IsAdmin: manifest.User.IsAdmin,
		}
		if manifest.User.Username != "" {
			u := manifest.User.Username
			userRow.Username = &u
		}
	}
	if kratosID, err := insertUserAndKratos(ctx, userRow, kratos); err != nil {
		warnings = append(warnings, fmt.Sprintf("user=%s reconstruct: %v", username, err))
	} else {
		applied = append(applied, fmt.Sprintf("panel-row: %s (kratos=%s)", username, kratosID))
	}

	// 2. PHP pools first — domains FK on php_pools.id.
	if meta != nil {
		for _, p := range meta.PHPPools {
			if err := insertPHPPool(ctx, userID, p); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s php-pool=%s: %v", username, p.ID, err))
				continue
			}
			applied = append(applied, fmt.Sprintf("php-pool: %s.%s", username, p.PHPVersion))
			for _, ovr := range p.IniOverrides {
				if err := insertPHPPoolOverride(ctx, p.ID, ovr); err != nil {
					warnings = append(warnings,
						fmt.Sprintf("user=%s pool-ini=%s: %v", username, ovr.Directive, err))
				}
			}
		}
	}

	// 3. Domains tree (per-domain SSL, mailboxes, forwarders, dnssec).
	domainIDByOldID := map[string]string{} // for app_installs domain remap
	if meta != nil {
		for _, dom := range meta.Domains {
			if !reconstructionDomainNameRE.MatchString(dom.Name) {
				warnings = append(warnings, fmt.Sprintf("user=%s domain=%s: invalid name", username, dom.Name))
				continue
			}
			if err := insertDomain(ctx, userID, dom); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s domain=%s: %v", username, dom.Name, err))
				continue
			}
			domainIDByOldID[dom.ID] = dom.ID
			applied = append(applied, fmt.Sprintf("domain: %s.%s", username, dom.Name))

			if dom.SSLCertificate != nil {
				if err := insertSSLCert(ctx, dom.ID, *dom.SSLCertificate); err != nil {
					warnings = append(warnings, fmt.Sprintf("user=%s ssl=%s: %v", username, dom.Name, err))
				} else {
					applied = append(applied, fmt.Sprintf("ssl: %s.%s", username, dom.Name))
				}
			}
			for _, mb := range dom.Mailboxes {
				if err := insertMailbox(ctx, dom.ID, mb); err != nil {
					warnings = append(warnings, fmt.Sprintf("user=%s mailbox=%s: %v", username, mb.LocalPart, err))
					continue
				}
				applied = append(applied, fmt.Sprintf("mailbox: %s", mb.EmailCached))
				if mb.Autoresponder != nil {
					if err := insertAutoresponder(ctx, mb.ID, *mb.Autoresponder); err != nil {
						warnings = append(warnings,
							fmt.Sprintf("user=%s autoresponder=%s: %v", username, mb.LocalPart, err))
					}
				}
				for _, sh := range mb.SharedWith {
					if err := insertMailboxShare(ctx, mb.ID, sh); err != nil {
						warnings = append(warnings,
							fmt.Sprintf("user=%s share=%s: %v", username, mb.LocalPart, err))
					}
				}
			}
			for _, fw := range dom.Forwarders {
				if err := insertForwarder(ctx, dom.ID, fw); err != nil {
					warnings = append(warnings,
						fmt.Sprintf("user=%s forwarder=%s: %v", username, fw.Target, err))
				}
			}
			for _, k := range dom.DNSSECKeys {
				if err := insertDNSSECKey(ctx, dom.ID, k); err != nil {
					warnings = append(warnings,
						fmt.Sprintf("user=%s dnssec=%d: %v", username, k.KeyTag, err))
				}
			}
		}
	}

	// 4. Databases (panel_db rows) + database_users + grants.
	dbIDByName := map[string]string{}
	if meta != nil {
		for _, db := range meta.Databases {
			dbIDByName[db.Name] = db.ID
			if err := insertPanelDatabase(ctx, userID, db); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s panel-db-row=%s: %v", username, db.Name, err))
				continue
			}
			applied = append(applied, fmt.Sprintf("panel-db-row: %s.%s", username, db.Name))
		}
		for _, du := range meta.DatabaseUsers {
			if err := insertPanelDatabaseUser(ctx, userID, du); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s db_user=%s: %v", username, du.Username, err))
				continue
			}
			applied = append(applied, fmt.Sprintf("db-user: %s.%s", username, du.Username))
			for _, g := range du.Grants {
				dbID := g.DatabaseID
				if dbID == "" {
					if mapped, ok := dbIDByName[g.DatabaseName]; ok {
						dbID = mapped
					}
				}
				if dbID == "" {
					warnings = append(warnings, fmt.Sprintf("user=%s grant=%s.%s: missing db_id",
						username, du.Username, g.DatabaseName))
					continue
				}
				if err := insertPanelGrant(ctx, du.ID, dbID, g); err != nil {
					warnings = append(warnings, fmt.Sprintf("user=%s grant=%s.%s: %v",
						username, du.Username, g.DatabaseName, err))
				}
			}
		}
	}

	// 5. application_installs (FK on domain + db_id).
	if meta != nil {
		for _, ai := range meta.AppInstalls {
			if _, ok := domainIDByOldID[ai.DomainID]; !ok {
				warnings = append(warnings,
					fmt.Sprintf("user=%s app=%s: domain_id %s not found", username, ai.ID, ai.DomainID))
				continue
			}
			if err := insertAppInstall(ctx, userID, ai); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s app=%s: %v", username, ai.ID, err))
				continue
			}
			applied = append(applied, fmt.Sprintf("app: %s.%s", username, ai.AppType))
		}
	}

	// 6. ssh_keys, cron_jobs, egress, limits — all hang off the user.
	if meta != nil {
		for _, k := range meta.SSHKeys {
			if err := insertSSHKey(ctx, userID, k); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s ssh_key=%s: %v", username, k.Name, err))
				continue
			}
			applied = append(applied, fmt.Sprintf("ssh-key: %s.%s", username, k.Name))
		}
		for _, j := range meta.CronJobs {
			if err := insertCronJob(ctx, userID, j); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s cron=%s: %v", username, j.Name, err))
				continue
			}
			applied = append(applied, fmt.Sprintf("cron: %s.%s", username, j.Name))
		}
		if meta.EgressPolicy != nil {
			if err := insertEgressPolicy(ctx, userID, *meta.EgressPolicy); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s egress: %v", username, err))
			} else {
				applied = append(applied, fmt.Sprintf("egress-policy: %s", username))
			}
		}
		for _, r := range meta.EgressRequests {
			if err := insertEgressRequest(ctx, userID, r); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s egress-req=%s: %v", username, r.ID, err))
			}
		}
		if meta.LimitOverride != nil {
			if err := insertLimitOverride(ctx, userID, *meta.LimitOverride); err != nil {
				warnings = append(warnings, fmt.Sprintf("user=%s limits: %v", username, err))
			} else {
				applied = append(applied, fmt.Sprintf("limits: %s", username))
			}
		}
	}

	return applied, warnings
}

// ─── INSERT helpers ──────────────────────────────────────────────────
//
// Every helper uses INSERT IGNORE so re-running a restore is safe.
// sqlEscape doubles single-quotes so a manually-crafted password
// can't break out of the literal. Caller wraps in single quotes.

// reusableKratosNID + reusableKratosSchema cache the per-host Kratos
// network-id and schema-id discovered from any existing identity row.
var (
	reusableKratosNID    string
	reusableKratosSchema string
)

func ensureKratosLookup(ctx context.Context) (string, string) {
	if reusableKratosNID != "" {
		return reusableKratosNID, reusableKratosSchema
	}
	out, err := execCommandContext(ctx, "mariadb",
		"--protocol=socket", "--socket=/run/mysqld/mysqld.sock",
		"-N", "-B",
		"-e", "SELECT nid, schema_id FROM jabali_kratos.identities WHERE state='active' LIMIT 1",
	).Output()
	if err != nil {
		return "", ""
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) < 2 {
		return "", ""
	}
	reusableKratosNID = parts[0]
	reusableKratosSchema = parts[1]
	return reusableKratosNID, reusableKratosSchema
}

func lookupKratosIdentityByEmail(ctx context.Context, email string) string {
	if email == "" {
		return ""
	}
	stmt := fmt.Sprintf(
		"SELECT id FROM jabali_kratos.identities WHERE JSON_EXTRACT(traits,'$.email')='\"%s\"' LIMIT 1",
		sqlEscape(email))
	out, err := execCommandContext(ctx, "mariadb",
		"--protocol=socket", "--socket=/run/mysqld/mysqld.sock",
		"-N", "-B",
		"-e", stmt,
	).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// insertUserAndKratos creates the panel users row AND the matching
// jabali_kratos.identities (+ identity_credentials when meta.Kratos is
// populated). Returns the kratos identity id used for the FK.
func insertUserAndKratos(ctx context.Context, u *backup.MetadataUser,
	k *backup.MetadataKratos) (string, error) {

	nid, schemaID := ensureKratosLookup(ctx)
	if nid == "" {
		return "", fmt.Errorf("no existing Kratos identity to copy nid/schema_id from")
	}

	// When k != nil and k.ExportedIdentity != "", parse the exported identity
	var kratosID, traits, state, availableAAL, usedSchema string
	if k != nil && k.ExportedIdentity != "" {
		var exported struct {
			ID             string          `json:"id"`
			Traits         json.RawMessage `json:"traits"`
			State          string          `json:"state"`
			AvailableAAL   string          `json:"available_aal"`
			SchemaID       string          `json:"schema_id"`
		}
		if err := json.Unmarshal([]byte(k.ExportedIdentity), &exported); err == nil {
			kratosID = exported.ID
			traits = string(exported.Traits)
			if exported.State != "" {
				state = exported.State
			}
			if exported.AvailableAAL != "" {
				availableAAL = exported.AvailableAAL
			}
			if exported.SchemaID != "" {
				usedSchema = exported.SchemaID
			}
		}
	}

	// Set defaults if not found in exported data
	if kratosID == "" {
		kratosID = lookupKratosIdentityByEmail(ctx, u.Email)
		if kratosID == "" {
			kratosID = newUUIDv4()
		}
	}

	if traits == "" {
		t := map[string]any{
			"email":    u.Email,
			"is_admin": u.IsAdmin,
		}
		if u.Username != nil && *u.Username != "" {
			t["username"] = *u.Username
		}
		body, _ := json.Marshal(t)
		traits = string(body)
	}

	if state == "" {
		state = "active"
	}
	if availableAAL == "" {
		availableAAL = "aal1"
	}
	if usedSchema == "" {
		usedSchema = schemaID
	}

	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_kratos.identities "+
			"(id, schema_id, traits, state, state_changed_at, created_at, updated_at, nid, available_aal) "+
			"VALUES ('%s','%s','%s','%s',NOW(),NOW(),NOW(),'%s','%s')",
		sqlEscape(kratosID), sqlEscape(usedSchema), sqlEscape(traits),
		sqlEscape(state), sqlEscape(nid), sqlEscape(availableAAL))
	if err := runMariaDBStmt(ctx, stmt); err != nil {
		return "", fmt.Errorf("kratos identity insert: %w", err)
	}

	// Credentials: preserve the source's password hash so login keeps
	// working without an admin reset email.
	// Extract credentials from ExportedIdentity if available
	if k != nil && k.ExportedIdentity != "" {
		var exported struct {
			Credentials []struct {
				ID                        string `json:"id"`
				Config                    string `json:"config"`
				IdentityCredentialTypeID string `json:"identity_credential_type_id"`
				Version                   int    `json:"version"`
			} `json:"credentials"`
		}
		if err := json.Unmarshal([]byte(k.ExportedIdentity), &exported); err == nil {
			for _, cred := range exported.Credentials {
				cstmt := fmt.Sprintf(
					"INSERT IGNORE INTO jabali_kratos.identity_credentials "+
						"(id, config, identity_credential_type_id, identity_id, created_at, updated_at, nid, version) "+
						"VALUES ('%s','%s','%s','%s',NOW(),NOW(),'%s',%d)",
					sqlEscape(cred.ID), sqlEscape(cred.Config),
					sqlEscape(cred.IdentityCredentialTypeID), sqlEscape(kratosID),
					sqlEscape(nid), cred.Version)
				if err := runMariaDBStmt(ctx, cstmt); err != nil {
					return kratosID, fmt.Errorf("kratos credential insert: %w", err)
				}
			}
		}
	}

	// jabali_panel.users
	username := ""
	if u.Username != nil {
		username = *u.Username
	}
	usernameLit := "NULL"
	if username != "" {
		usernameLit = "'" + sqlEscape(username) + "'"
	}
	packageLit := "NULL"
	if u.PackageID != nil {
		packageLit = "'" + sqlEscape(*u.PackageID) + "'"
	}
	linuxLit := "NULL"
	if u.LinuxUID != nil {
		linuxLit = fmt.Sprintf("%d", *u.LinuxUID)
	}
	mysqlULit := "NULL"
	if u.MysqladminUsername != nil {
		mysqlULit = "'" + sqlEscape(*u.MysqladminUsername) + "'"
	}
	mysqlPwLit := "NULL"
	if len(u.MysqladminPasswordEnc) > 0 {
		mysqlPwLit = fmt.Sprintf("UNHEX('%x')", u.MysqladminPasswordEnc)
	}
	mysqlProvLit := "NULL"
	if u.MysqladminProvisionedAt != "" {
		mysqlProvLit = "'" + sqlEscape(u.MysqladminProvisionedAt) + "'"
	}
	pwHash := u.PasswordHash // varchar NOT NULL
	stmt = fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.users "+
			"(id, email, username, name_first, name_last, password_hash, is_admin, "+
			"package_id, linux_uid, mysqladmin_username, mysqladmin_password_enc, "+
			"mysqladmin_provisioned_at, kratos_identity_id, created_at, updated_at) "+
			"VALUES ('%s','%s',%s,'%s','%s','%s',%d,%s,%s,%s,%s,%s,'%s',NOW(),NOW())",
		sqlEscape(u.ID), sqlEscape(u.Email), usernameLit,
		sqlEscape(u.NameFirst), sqlEscape(u.NameLast), sqlEscape(pwHash),
		boolToInt(u.IsAdmin),
		packageLit, linuxLit, mysqlULit, mysqlPwLit, mysqlProvLit,
		sqlEscape(kratosID))
	if err := runMariaDBStmt(ctx, stmt); err != nil {
		return kratosID, fmt.Errorf("panel users insert: %w", err)
	}
	return kratosID, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// newUUIDv4 returns an RFC4122 v4 UUID using crypto/rand.
func newUUIDv4() string {
	var b [16]byte
	if _, err := cryptoRandRead(b[:]); err != nil {
		now := time.Now().UnixNano()
		for i := 0; i < 16; i++ {
			b[i] = byte(now >> (uint(i) * 4))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hex := "0123456789abcdef"
	out := make([]byte, 36)
	pos := 0
	for i, by := range b {
		out[pos] = hex[by>>4]
		out[pos+1] = hex[by&0x0f]
		pos += 2
		if i == 3 || i == 5 || i == 7 || i == 9 {
			out[pos] = '-'
			pos++
		}
	}
	return string(out)
}

// optStringPtr formats a *string as a SQL literal (NULL when nil).
func optStringPtr(s *string) string {
	if s == nil {
		return "NULL"
	}
	return "'" + sqlEscape(*s) + "'"
}

// optIntPtr formats a *int as SQL literal (NULL when nil).
func optIntPtr(p *int) string {
	if p == nil {
		return "NULL"
	}
	return fmt.Sprintf("%d", *p)
}

// optUint32Ptr formats a *uint32 as SQL literal (NULL when nil).
func optUint32Ptr(p *uint32) string {
	if p == nil {
		return "NULL"
	}
	return fmt.Sprintf("%d", *p)
}

// optUint64Ptr formats a *uint64 as SQL literal (NULL when nil).
func optUint64Ptr(p *uint64) string {
	if p == nil {
		return "NULL"
	}
	return fmt.Sprintf("%d", *p)
}

// optTimeRFC formats an RFC3339 string as SQL DATETIME(6) literal.
func optTimeRFC(s string) string {
	if s == "" {
		return "NULL"
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "NULL"
	}
	return "'" + t.Format("2006-01-02 15:04:05.000000") + "'"
}

func optTextPtr(p *string) string {
	if p == nil {
		return "NULL"
	}
	return "'" + sqlEscape(*p) + "'"
}

func insertPHPPool(ctx context.Context, userID string, p backup.MetadataPHPPool) error {
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.php_pools "+
			"(id, user_id, php_version, pm_mode, pm_max_children, "+
			"process_idle_timeout_seconds, status, created_at, updated_at) "+
			"VALUES ('%s','%s','%s','%s',%d,%d,'%s',NOW(),NOW())",
		sqlEscape(p.ID), sqlEscape(userID), sqlEscape(p.PHPVersion),
		sqlEscape(p.PmMode), p.PmMaxChildren, p.ProcessIdleTimeoutSeconds,
		sqlEscape(p.Status))
	return runMariaDBStmt(ctx, stmt)
}

func insertPHPPoolOverride(ctx context.Context, poolID string, o backup.MetadataPHPPoolIniOverride) error {
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.php_pool_ini_overrides "+
			"(id, pool_id, directive, value, kind, created_at, updated_at) "+
			"VALUES ('%s','%s','%s','%s','%s',NOW(),NOW())",
		sqlEscape(o.ID), sqlEscape(poolID),
		sqlEscape(o.Directive), sqlEscape(o.Value), sqlEscape(o.Kind))
	return runMariaDBStmt(ctx, stmt)
}

func insertDomain(ctx context.Context, userID string, d backup.MetadataDomain) error {
	pageRedirects := "NULL"
	if d.PageRedirects != "" {
		pageRedirects = "'" + sqlEscape(d.PageRedirects) + "'"
	}
	nginxRules := "NULL"
	if d.NginxRules != "" {
		nginxRules = "'" + sqlEscape(d.NginxRules) + "'"
	}
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.domains "+
			"(id, user_id, name, doc_root, is_enabled, nginx_custom_directives, "+
			"redirect_all_to, redirect_all_type, page_redirects, nginx_rules, "+
			"index_priority, ssl_enabled, php_pool_id, php_memory_limit, "+
			"php_upload_max_filesize, php_post_max_size, php_max_input_vars, "+
			"php_max_execution_time, php_max_input_time, rate_limit_rps, "+
			"connection_limit, listen_ipv4_id, listen_ipv6_id, email_enabled, "+
			"dkim_selector, dkim_public_key, email_enabled_at, is_panel_primary, "+
			"catchall_target, disclaimer_enabled, disclaimer_text, dnssec_enabled, "+
			"dnssec_enabled_at, ghost_state, created_at, updated_at) "+
			"VALUES ('%s','%s','%s','%s',%d,%s,%s,%s,%s,%s,'%s',%d,%s,%s,%s,%s,%s,%s,%s,%d,%d,%s,%s,%d,%s,%s,%s,%d,%s,%d,%s,%d,%s,'unchecked',NOW(),NOW())",
		sqlEscape(d.ID), sqlEscape(userID), sqlEscape(d.Name), sqlEscape(d.DocRoot),
		boolToInt(d.IsEnabled), optTextPtr(d.NginxCustomDirectives),
		optStringPtr(d.RedirectAllTo), optStringPtr(d.RedirectAllType),
		pageRedirects, nginxRules,
		sqlEscape(d.IndexPriority), boolToInt(d.SSLEnabled),
		optStringPtr(d.PHPPoolID),
		optStringPtr(d.PHPMemoryLimit), optStringPtr(d.PHPUploadMaxFilesize),
		optStringPtr(d.PHPPostMaxSize),
		optIntPtr(d.PHPMaxInputVars), optIntPtr(d.PHPMaxExecutionTime),
		optIntPtr(d.PHPMaxInputTime),
		d.RateLimitRPS, d.ConnectionLimit,
		optUint64Ptr(d.ListenIPv4ID), optUint64Ptr(d.ListenIPv6ID),
		boolToInt(d.EmailEnabled),
		optStringPtr(d.DkimSelector), optTextPtr(d.DkimPublicKey),
		optTimeRFC(d.EmailEnabledAt),
		boolToInt(d.IsPanelPrimary),
		optStringPtr(d.CatchallTarget),
		boolToInt(d.DisclaimerEnabled), optTextPtr(d.DisclaimerText),
		boolToInt(d.DNSSECEnabled),
		optTimeRFC(d.DNSSECEnabledAt))
	return runMariaDBStmt(ctx, stmt)
}

func insertSSLCert(ctx context.Context, domainID string, c backup.MetadataSSLCert) error {
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.ssl_certificates "+
			"(id, domain_id, status, issued_at, expires_at, renewal_count, "+
			"last_renewed_at, last_error, staging, cert_path, key_path, "+
			"retry_count, created_at, updated_at) "+
			"VALUES ('%s','%s','%s',%s,%s,%d,%s,%s,%d,%s,%s,0,NOW(),NOW())",
		sqlEscape(c.ID), sqlEscape(domainID), sqlEscape(c.Status),
		optTimeRFC(c.IssuedAt), optTimeRFC(c.ExpiresAt), c.RenewalCount,
		optTimeRFC(c.LastRenewedAt), optTextPtr(c.LastError),
		boolToInt(c.Staging),
		optStringPtr(c.CertPath), optStringPtr(c.KeyPath))
	return runMariaDBStmt(ctx, stmt)
}

func insertMailbox(ctx context.Context, domainID string, mb backup.MetadataMailbox) error {
	pwEnc := "NULL"
	if len(mb.PasswordEnc) > 0 {
		pwEnc = fmt.Sprintf("UNHEX('%x')", mb.PasswordEnc)
	}
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.mailboxes "+
			"(id, domain_id, local_part, password_hash, password_enc, "+
			"quota_bytes, is_disabled, created_at, updated_at) "+
			"VALUES ('%s','%s','%s','%s',%s,%d,%d,NOW(),NOW())",
		sqlEscape(mb.ID), sqlEscape(domainID), sqlEscape(mb.LocalPart),
		sqlEscape(mb.PasswordHash), pwEnc, mb.QuotaBytes,
		boolToInt(mb.IsDisabled))
	return runMariaDBStmt(ctx, stmt)
}

func insertAutoresponder(ctx context.Context, mailboxID string, a backup.MetadataAutoresponder) error {
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.email_autoresponders "+
			"(mailbox_id, enabled, from_date, to_date, subject, text_body, "+
			"html_body, managed_by, updated_at) "+
			"VALUES ('%s',%d,%s,%s,%s,%s,%s,'m6.5',NOW())",
		sqlEscape(mailboxID), boolToInt(a.Enabled),
		optTimeRFC(a.FromDate), optTimeRFC(a.ToDate),
		optStringPtr(a.Subject), optTextPtr(a.TextBody), optTextPtr(a.HTMLBody))
	return runMariaDBStmt(ctx, stmt)
}

func insertMailboxShare(ctx context.Context, ownerMailboxID string, sh backup.MetadataMailboxShare) error {
	rights := sh.Rights
	if rights == "" {
		rights = "{}"
	}
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.mailbox_shares "+
			"(id, owner_mailbox_id, shared_with_mailbox_id, rights, managed_by, created_at) "+
			"VALUES ('%s','%s','%s','%s','m6.5',NOW())",
		sqlEscape(sh.ID), sqlEscape(ownerMailboxID),
		sqlEscape(sh.SharedWithMailboxID), sqlEscape(rights))
	return runMariaDBStmt(ctx, stmt)
}

func insertForwarder(ctx context.Context, domainID string, f backup.MetadataForwarder) error {
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.email_forwarders "+
			"(id, mailbox_id, domain_id, type, local_part, target, enabled, "+
			"managed_by, created_at, updated_at) "+
			"VALUES ('%s',%s,'%s','%s',%s,'%s',%d,'m6.5',NOW(),NOW())",
		sqlEscape(f.ID), optStringPtr(f.MailboxID), sqlEscape(domainID),
		sqlEscape(f.Type), optStringPtr(f.LocalPart), sqlEscape(f.Target),
		boolToInt(f.Enabled))
	return runMariaDBStmt(ctx, stmt)
}

func insertDNSSECKey(ctx context.Context, domainID string, k backup.MetadataDNSSECKey) error {
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.domain_dnssec_keys "+
			"(domain_id, key_tag, key_type, algorithm, public_key, active, observed_at) "+
			"VALUES ('%s',%d,'%s',%d,'%s',%d,NOW())",
		sqlEscape(domainID), k.KeyTag,
		sqlEscape(k.KeyType), k.Algorithm,
		sqlEscape(k.PublicKey), boolToInt(k.Active))
	return runMariaDBStmt(ctx, stmt)
}

func insertPanelDatabase(ctx context.Context, userID string, db backup.MetadataDatabase) error {
	if !dbNameRE.MatchString(db.Name) {
		return fmt.Errorf("invalid db name %q", db.Name)
	}
	engine := db.Engine
	if engine == "" {
		engine = "mariadb"
	}
	charset := db.Charset
	if charset == "" {
		charset = "utf8mb4"
	}
	collation := db.Collation
	if collation == "" {
		collation = "utf8mb4_unicode_ci"
	}
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.databases "+
			"(id, user_id, name, engine, charset, collation, created_at, updated_at) "+
			"VALUES ('%s','%s','%s','%s','%s','%s',NOW(),NOW())",
		sqlEscape(db.ID), sqlEscape(userID), sqlEscape(db.Name),
		sqlEscape(engine), sqlEscape(charset), sqlEscape(collation))
	return runMariaDBStmt(ctx, stmt)
}

func insertPanelDatabaseUser(ctx context.Context, userID string, du backup.MetadataDatabaseUser) error {
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.database_users "+
			"(id, user_id, username, password_hash, created_at, updated_at) "+
			"VALUES ('%s','%s','%s','%s',NOW(),NOW())",
		sqlEscape(du.ID), sqlEscape(userID),
		sqlEscape(du.Username), sqlEscape(du.PasswordHash))
	return runMariaDBStmt(ctx, stmt)
}

func insertPanelGrant(ctx context.Context, dbUserID, dbID string, g backup.MetadataDatabaseUserGrant) error {
	level := g.GrantLevel
	if level == "" {
		level = "rw"
	}
	privs := g.Privileges
	if privs == "" {
		privs = "ALL"
	}
	id := g.ID
	if id == "" {
		id = newRestoreULID()
	}
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.database_user_grants "+
			"(id, database_id, database_user_id, grant_level, privileges, created_at, updated_at) "+
			"VALUES ('%s','%s','%s','%s','%s',NOW(),NOW())",
		sqlEscape(id), sqlEscape(dbID), sqlEscape(dbUserID),
		sqlEscape(level), sqlEscape(privs))
	return runMariaDBStmt(ctx, stmt)
}

func insertAppInstall(ctx context.Context, userID string, ai backup.MetadataAppInstall) error {
	dbIDLit := "NULL"
	if ai.DBID != nil && *ai.DBID != "" {
		dbIDLit = "'" + sqlEscape(*ai.DBID) + "'"
	}
	versionLit := "NULL"
	if ai.Version != nil && *ai.Version != "" {
		versionLit = "'" + sqlEscape(*ai.Version) + "'"
	}
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.application_installs "+
			"(id, user_id, domain_id, db_id, version, admin_username, admin_email, "+
			"locale, status, last_error, use_www, subdirectory, app_type, created_at, updated_at) "+
			"VALUES ('%s','%s','%s',%s,%s,'%s','%s','%s','%s','',%d,'%s','%s',NOW(),NOW())",
		sqlEscape(ai.ID), sqlEscape(userID), sqlEscape(ai.DomainID),
		dbIDLit, versionLit,
		sqlEscape(ai.AdminUsername), sqlEscape(ai.AdminEmail),
		sqlEscape(ai.Locale), sqlEscape(ai.Status),
		boolToInt(ai.UseWWW), sqlEscape(ai.Subdirectory), sqlEscape(ai.AppType))
	return runMariaDBStmt(ctx, stmt)
}

func insertSSHKey(ctx context.Context, userID string, k backup.MetadataSSHKey) error {
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.ssh_keys "+
			"(id, user_id, name, public_key, fingerprint, created_at) "+
			"VALUES ('%s','%s','%s','%s','%s',NOW())",
		sqlEscape(k.ID), sqlEscape(userID), sqlEscape(k.Name),
		sqlEscape(k.PublicKey), sqlEscape(k.Fingerprint))
	return runMariaDBStmt(ctx, stmt)
}

func insertCronJob(ctx context.Context, userID string, j backup.MetadataCronJob) error {
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.cron_jobs "+
			"(id, user_id, name, command, schedule, enabled, created_at, updated_at) "+
			"VALUES ('%s','%s','%s','%s','%s',%d,NOW(),NOW())",
		sqlEscape(j.ID), sqlEscape(userID), sqlEscape(j.Name),
		sqlEscape(j.Command), sqlEscape(j.Schedule), boolToInt(j.Enabled))
	return runMariaDBStmt(ctx, stmt)
}

func insertEgressPolicy(ctx context.Context, userID string, p backup.MetadataEgressPolicy) error {
	allowed := p.AllowedExtra
	if allowed == "" {
		allowed = "[]"
	}
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.user_egress_policies "+
			"(user_id, state, allowed_extra, learning_started_at, updated_at) "+
			"VALUES ('%s','%s','%s',%s,NOW())",
		sqlEscape(userID), sqlEscape(p.State), sqlEscape(allowed),
		optTimeRFC(p.LearningStartedAt))
	return runMariaDBStmt(ctx, stmt)
}

func insertEgressRequest(ctx context.Context, userID string, r backup.MetadataEgressRequest) error {
	portLit := "NULL"
	if r.Port != nil {
		portLit = fmt.Sprintf("%d", *r.Port)
	}
	reviewedByLit := "NULL"
	if r.ReviewedBy != "" {
		reviewedByLit = "'" + sqlEscape(r.ReviewedBy) + "'"
	}
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.user_egress_requests "+
			"(id, user_id, cidr, port, protocol, reason, status, reviewed_by, "+
			"decided_at, created_at) "+
			"VALUES ('%s','%s','%s',%s,'%s','%s','%s',%s,%s,NOW())",
		sqlEscape(r.ID), sqlEscape(userID), sqlEscape(r.CIDR), portLit,
		sqlEscape(r.Protocol), sqlEscape(r.Reason), sqlEscape(r.Status),
		reviewedByLit, optTimeRFC(r.DecidedAt))
	return runMariaDBStmt(ctx, stmt)
}

func insertLimitOverride(ctx context.Context, userID string, lo backup.MetadataLimitOverride) error {
	stmt := fmt.Sprintf(
		"INSERT IGNORE INTO jabali_panel.user_limit_overrides "+
			"(user_id, disk_quota_mb, cpu_quota_percent, memory_limit_mb, "+
			"io_read_mbps, io_write_mbps, max_tasks, updated_at) "+
			"VALUES ('%s',%s,%s,%s,%s,%s,%s,NOW())",
		sqlEscape(userID),
		optUint32Ptr(lo.DiskQuotaMB), optUint32Ptr(lo.CPUQuotaPercent),
		optUint32Ptr(lo.MemoryLimitMB),
		optUint32Ptr(lo.IOReadMbps), optUint32Ptr(lo.IOWriteMbps),
		optUint32Ptr(lo.MaxTasks))
	return runMariaDBStmt(ctx, stmt)
}
