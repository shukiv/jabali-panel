package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/phpext"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/uploadedbackups"
)

// GH #1993: the restore preflight. Before a restore from an uploaded file,
// the panel checks the backup against this server. A PHP version the
// account's sites use and this server lacks blocks the restore. PHP
// extensions this server lacks are a warning. So are PostgreSQL, mail, DNS
// and Docker apps turned off here: the restore leaves those parts out.

// Preflight check levels.
const (
	preflightOK    = "ok"
	preflightInfo  = "info"
	preflightWarn  = "warn"
	preflightBlock = "block"
)

// restorePreflightCheck is one line of a restore preflight.
type restorePreflightCheck struct {
	// Area is what the line is about: agent, backup, settings, php,
	// php_extensions, postgres, mail, dns or docker.
	Area    string `json:"area"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// restorePreflight is what the restore drawer shows before Restore. Blocked
// means a check blocks the restore.
type restorePreflight struct {
	Blocked bool                    `json:"blocked"`
	Checks  []restorePreflightCheck `json:"checks"`
	// skips are the parts the restore leaves out.
	skips restoreSkips
}

// restoreSkips names the parts of a backup a restore leaves out because they
// are turned off on this server. The zero value skips nothing.
type restoreSkips struct {
	postgres, mail, dns, docker bool
	// notes are the restore report's lines for what the backup has of the
	// parts left out.
	notes []string
}

// uploadInspect is the part of backup.inspect_uploaded_tar's result the panel
// reads.
type uploadInspect struct {
	User struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		IsAdmin  bool   `json:"is_admin"`
	} `json:"user"`
	Components []string `json:"components"`
	// Summary is nil when the archive has no readable metadata.
	Summary *internalbackup.BundleSummary `json:"summary"`
	// PreflightSupported is false on an agent too old to read Summary or
	// honour skip_postgres.
	PreflightSupported bool `json:"preflight_supported"`
}

// restorePreflightFacts is what the preflight knows about this server.
type restorePreflightFacts struct {
	// settingsRead is false when the server settings couldn't be read.
	settingsRead                bool
	postgres, mail, dns, docker bool
	// phpVersions are the installed PHP versions; nil when they couldn't be
	// read.
	phpVersions map[string]bool
	// phpExtensions holds, for each installed version the backup uses, the
	// extensions it has (enabled or built in). A version that couldn't be
	// read is missing.
	phpExtensions map[string]map[string]bool
}

// inspectWriteBudget is how long a request that inspects an uploaded archive
// may run. The agent streams the archive until it has read the manifest and
// the metadata, which can take most of the agent call's own limit.
const inspectWriteBudget = agent.DefaultTimeout + 10*time.Second

// computeRestorePreflight checks the inspected backup ins against the server
// facts f. Pure: unit-tested.
func computeRestorePreflight(ins uploadInspect, f restorePreflightFacts) restorePreflight {
	p := restorePreflight{Checks: []restorePreflightCheck{}}
	add := func(area, level, msg string) {
		p.Checks = append(p.Checks, restorePreflightCheck{Area: area, Level: level, Message: msg})
		if level == preflightBlock {
			p.Blocked = true
		}
	}
	if !ins.PreflightSupported {
		add("agent", preflightBlock, "This server's agent is too old to check a backup before a restore. Update Jabali, then try again.")
		return p
	}
	if !f.settingsRead {
		add("settings", preflightBlock, "This server's settings couldn't be read, so the backup couldn't be checked. Try again.")
		return p
	}
	// A part turned off here is left out whatever the summary says: it is
	// read from the uploader's file, and the archive may hold more.
	p.skips = restoreSkips{postgres: !f.postgres, mail: !f.mail, dns: !f.dns, docker: !f.docker}

	s := ins.Summary
	if s == nil {
		add("backup", preflightInfo, "The backup has no account details, so there is nothing to check against this server.")
		return p
	}
	preflightPHP(s, f, add)

	if n := len(s.PostgresDatabases); n > 0 || s.PostgresUsers > 0 {
		what := joinParts(countNoun(n, "PostgreSQL database", "PostgreSQL databases")+namesInParens(s.PostgresDatabases, n),
			countNoun(s.PostgresUsers, "PostgreSQL database user", "PostgreSQL database users"))
		if p.skips.postgres {
			add("postgres", preflightWarn, "PostgreSQL is turned off on this server, so the backup's "+what+" won't be restored. To restore them, turn PostgreSQL on under Server Settings first.")
			p.skips.notes = append(p.skips.notes, "PostgreSQL ("+what+"): not restored: PostgreSQL is turned off on this server")
		} else {
			add("postgres", preflightOK, "The backup has "+what+". PostgreSQL is on here.")
		}
	}
	if s.Mailboxes > 0 || s.Forwarders > 0 {
		what := joinParts(countNoun(s.Mailboxes, "mailbox", "mailboxes"), countNoun(s.Forwarders, "forwarder", "forwarders"))
		if p.skips.mail {
			add("mail", preflightWarn, "Mail is turned off on this server, so the backup's "+what+" won't be restored. The domains keep their mail settings.")
			p.skips.notes = append(p.skips.notes, "mail ("+what+"): not restored: mail is turned off on this server")
		} else {
			add("mail", preflightOK, "The backup has "+what+". Mail is on here.")
		}
	}
	if s.DNSRecords > 0 {
		what := countNoun(s.DNSRecords, "custom DNS record", "custom DNS records")
		if p.skips.dns {
			add("dns", preflightWarn, "DNS is turned off on this server, so the backup's "+what+" won't be restored.")
			p.skips.notes = append(p.skips.notes, "DNS ("+what+"): not restored: DNS is turned off on this server")
		} else {
			add("dns", preflightOK, "The backup has "+what+". DNS is on here.")
		}
	}
	if n := len(s.DockerApps); n > 0 {
		what := countNoun(n, "Docker app", "Docker apps") + namesInParens(s.DockerApps, n)
		if p.skips.docker {
			add("docker", preflightWarn, "Docker apps for users are turned off on this server, so the backup's "+what+" won't be restored.")
			p.skips.notes = append(p.skips.notes, "Docker apps ("+what+"): not restored: Docker apps for users are turned off on this server")
		} else {
			add("docker", preflightOK, "The backup has "+what+". Docker apps for users are on here.")
		}
	}
	return p
}

// preflightPHP adds the PHP version and extension lines.
func preflightPHP(s *internalbackup.BundleSummary, f restorePreflightFacts, add func(area, level, msg string)) {
	if len(s.PHPVersions) == 0 {
		return
	}
	if f.phpVersions == nil {
		add("php", preflightBlock, "This server's PHP versions couldn't be read, so the restore couldn't check the ones the account's sites use. Try again.")
		return
	}
	var have, missing []string
	for _, v := range s.PHPVersions {
		if f.phpVersions[v] {
			have = append(have, v)
		} else {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		verb, them := "isn't", "it"
		if len(missing) > 1 {
			verb, them = "aren't", "them"
		}
		add("php", preflightBlock, fmt.Sprintf("PHP %s %s installed on this server, and the account's sites use %s. Install %s under PHP Versions, then restore.",
			joinAnd(missing), verb, them, them))
	} else {
		verb := "is"
		if len(have) > 1 {
			verb = "are"
		}
		add("php", preflightOK, fmt.Sprintf("PHP %s, which the account's sites use, %s installed.", joinAnd(have), verb))
	}
	if len(have) == 0 {
		return
	}
	if s.PHPExtensions == nil {
		add("php_extensions", preflightInfo, "The backup doesn't record which PHP extensions the account's sites had (it was made before Jabali recorded them), so they couldn't be checked.")
		return
	}
	lacking := false
	var unchecked []string
	for _, v := range have {
		want, recorded := s.PHPExtensions[v]
		present, read := f.phpExtensions[v]
		if !recorded || !read {
			unchecked = append(unchecked, v)
			continue
		}
		var miss []string
		for _, e := range want {
			if !present[e] {
				miss = append(miss, e)
			}
		}
		if len(miss) == 0 {
			continue
		}
		lacking = true
		them := "it"
		if len(miss) > 1 {
			them = "them"
		}
		add("php_extensions", preflightWarn, fmt.Sprintf("PHP %s here lacks %s, which the source server had enabled. Sites that need %s won't work until you enable %s under PHP Versions.",
			v, joinAnd(miss), them, them))
	}
	if len(unchecked) > 0 {
		add("php_extensions", preflightInfo, "The PHP extensions of PHP "+joinAnd(unchecked)+" couldn't be checked.")
	} else if !lacking {
		add("php_extensions", preflightOK, "The PHP extensions the account's sites had are enabled here.")
	}
}

// restorePreflightFacts reads what the preflight of the backup summarized as
// s needs to know about this server.
func (h *backupHandler) restorePreflightFacts(ctx context.Context, s *internalbackup.BundleSummary) restorePreflightFacts {
	var f restorePreflightFacts
	if h.cfg.ServerSettings != nil {
		if st, err := h.cfg.ServerSettings.Get(ctx); err == nil && st != nil {
			f.settingsRead = true
			f.postgres, f.mail, f.dns = st.PostgresEnabled, st.MailEnabled, st.DNSEnabled
			// A user's docker app needs the engine and the tenant setup.
			f.docker = st.DockerMarketplaceEnabled && st.DockerAppsForUsersEnabled
		}
	}
	if s == nil || len(s.PHPVersions) == 0 || h.cfg.Agent == nil {
		return f
	}
	installed, ok := installedPHPVersionSet(ctx, h.cfg.Agent)
	if !ok {
		return f
	}
	f.phpVersions = installed
	if s.PHPExtensions == nil {
		return f
	}
	f.phpExtensions = map[string]map[string]bool{}
	for _, v := range s.PHPVersions {
		if !installed[v] || !phpext.ValidVersion(v) {
			continue
		}
		raw, err := h.cfg.Agent.Call(ctx, "php.ext.list", map[string]string{"version": v})
		if err != nil {
			continue
		}
		var resp struct {
			Extensions []struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
				BuiltIn bool   `json:"built_in"`
			} `json:"extensions"`
		}
		if json.Unmarshal(raw, &resp) != nil {
			continue
		}
		present := map[string]bool{}
		for _, e := range resp.Extensions {
			if e.Enabled || e.BuiltIn {
				present[e.Name] = true
			}
		}
		f.phpExtensions[v] = present
	}
	return f
}

// inspectUpload runs backup.inspect_uploaded_tar on the uploaded archive at
// path and parses what the panel reads. raw is the agent's reply.
func (h *backupHandler) inspectUpload(ctx context.Context, path string) (ins uploadInspect, raw json.RawMessage, err error) {
	raw, err = h.cfg.Agent.Call(ctx, "backup.inspect_uploaded_tar", map[string]string{"tar_path": path})
	if err != nil {
		return ins, nil, err
	}
	if err := json.Unmarshal(raw, &ins); err != nil {
		return ins, raw, fmt.Errorf("parse inspect result: %w", err)
	}
	return ins, raw, nil
}

// preflightFor is the preflight of the inspected backup ins.
func (h *backupHandler) preflightFor(ctx context.Context, ins uploadInspect) restorePreflight {
	return computeRestorePreflight(ins, h.restorePreflightFacts(ctx, ins.Summary))
}

// restorePreflightBlocked writes the 409 for a restore its preflight blocks.
func restorePreflightBlocked(c *gin.Context, p restorePreflight) {
	detail := "the backup can't be restored on this server yet"
	for _, ch := range p.Checks {
		if ch.Level == preflightBlock {
			detail = ch.Message
			break
		}
	}
	c.JSON(http.StatusConflict, gin.H{"error": "restore_preflight_blocked", "detail": detail, "preflight": p})
}

// gateUploadRestore inspects the uploaded archive at path and runs its
// preflight before a restore. On a block or an error it writes the response
// and returns ok=false.
func (h *backupHandler) gateUploadRestore(c *gin.Context, path string) (uploadInspect, restorePreflight, bool) {
	extendWriteDeadline(c, inspectWriteBudget)
	ins, _, err := h.inspectUpload(c.Request.Context(), path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "not_an_account_backup",
			"detail": agentReason(err, "the file could not be read as a Jabali account backup")})
		return ins, restorePreflight{}, false
	}
	p := h.preflightFor(c.Request.Context(), ins)
	if p.Blocked {
		restorePreflightBlocked(c, p)
		return ins, p, false
	}
	return ins, p, true
}

// uploadedBackupPreflight handles GET /admin/uploaded-backups/:id/preflight:
// the preflight of a kept uploaded backup, which the restore drawer shows
// before Restore.
func (h *backupHandler) uploadedBackupPreflight(c *gin.Context) {
	b, ok := h.uploadedBackupParam(c)
	if !ok {
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	extendWriteDeadline(c, inspectWriteBudget)
	ins, _, err := h.inspectUpload(c.Request.Context(), uploadedbackups.Path(b.ID))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "not_an_account_backup",
			"detail": agentReason(err, "the file could not be read as a Jabali account backup")})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": h.preflightFor(c.Request.Context(), ins)})
}

// countNoun is "1 mailbox" or "3 mailboxes"; "" for zero.
func countNoun(n int, one, many string) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// namesInParens is " (a, b, c)" for up to five names, with "and N more"
// when there are more; "" when there are none.
func namesInParens(names []string, total int) string {
	if len(names) == 0 {
		return ""
	}
	shown := names
	if len(shown) > 5 {
		shown = shown[:5]
	}
	out := strings.Join(shown, ", ")
	if rest := total - len(shown); rest > 0 {
		out += fmt.Sprintf(" and %d more", rest)
	}
	return " (" + out + ")"
}

// joinParts joins the non-empty parts with "and".
func joinParts(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return joinAnd(keep)
}

// joinAnd is "a", "a and b" or "a, b and c".
func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}
