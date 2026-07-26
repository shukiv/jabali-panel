package api

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/directadmin"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/hestiacp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/plesk"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/wordpressplugin"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/wordpressssh"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// panelDiscoverer returns the account-panel Discoverer for an SSH source kind
// (cPanel/WHM, DirectAdmin, HestiaCP), or nil for non-panel kinds. GH #665.
func panelDiscoverer(kind string, allowPrivate bool) migrate.Discoverer {
	switch kind {
	case models.MigrationSourceCpanel, models.MigrationSourceWHMpkgacct:
		d := cpanel.New()
		d.AllowPrivate = allowPrivate
		return d
	case models.MigrationSourceDirectAdmin:
		d := directadmin.New()
		d.AllowPrivate = allowPrivate
		return d
	case models.MigrationSourceHestia:
		d := hestiacp.New()
		d.AllowPrivate = allowPrivate
		return d
	case models.MigrationSourcePlesk:
		d := plesk.New()
		d.AllowPrivate = allowPrivate
		return d
	}
	return nil
}

// testConnection (GH #665) — the wizard's "Test connection": handshake the
// source and return detected capabilities/counts, or a clear error, BEFORE any
// heavy work. Dispatches by source kind: panels via their Discoverer
// (Connect + ListAccounts → account/domain/size counts); wordpress_ssh via
// Connect + DiscoverWordPress; wordpress_plugin via ping + manifest.
func (h *adminMigrationsHandler) testConnection(c *gin.Context) {
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	allowPrivate := false
	if h.cfg.Settings != nil {
		if st, sErr := h.cfg.Settings.Get(c.Request.Context()); sErr == nil && st != nil {
			allowPrivate = st.MigrationAllowPrivateHosts
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	secret := migrate.SecretRef{Path: filepath.Join(migrate.SecretsDir, job.ID+".env")}

	switch job.SourceKind {
	case models.MigrationSourceWordPressPlugin:
		token := readSecretValue(secret.Path, "PLUGIN_TOKEN")
		if token == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no_token"})
			return
		}
		site := job.SourceHost
		if !strings.HasPrefix(site, "http") {
			site = "https://" + site
		}
		cli := wordpressplugin.New(site, token, allowPrivate)
		ping, err := cli.PingInfo(ctx)
		if err != nil {
			respondAgentErr(c, "handshake_failed", err)
			return
		}
		facts, err := cli.Manifest(ctx)
		if err != nil {
			respondAgentErr(c, "manifest_failed", err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "kind": "wordpress_plugin", "panel": "WordPress",
			"version": ping.Version, "needs_update": ping.Version < "0.1.2",
			"siteurl": facts.SiteURL, "wp_version": facts.WPVersion,
			"domains": 1, "databases": 1, "bytes": facts.DBBytes + facts.FileBytes})
		return

	case models.MigrationSourceWordPressSSH:
		sess, err := wordpressssh.Connect(ctx, job.SourceHost, 0, sshUserOrRoot(job.SourceUser), secret, allowPrivate)
		if err != nil {
			respondMigrateConnectErr(c, err)
			return
		}
		defer sess.Close()
		hint := ""
		if job.SourcePath != nil {
			hint = *job.SourcePath
		}
		facts, err := wordpressssh.DiscoverWordPress(ctx, sess, hint)
		if err != nil {
			respondAgentErr(c, "discover_failed", err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "kind": "wordpress_ssh", "panel": "WordPress",
			"wp_version": facts.WPVersion, "siteurl": facts.SiteURL,
			"domains": 1, "databases": 1, "bytes": facts.BytesTotal})
		return
	}

	// Account panels (cPanel/WHM, DirectAdmin, HestiaCP).
	d := panelDiscoverer(job.SourceKind, allowPrivate)
	if d == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_kind"})
		return
	}
	migrate.ApplyPort(d, job.SourcePort) // GH #429: test/describe previously ignored the custom SSH port (only the run path applied it)
	sess, err := d.Connect(ctx, job.SourceHost, sshUserOrRoot(job.SourceUser), secret)
	if err != nil {
		respondMigrateConnectErr(c, err)
		return
	}
	defer func() { _ = d.Close(ctx, sess) }()
	accts, err := d.ListAccounts(ctx, sess)
	if err != nil {
		respondAgentErr(c, "list_failed", err)
		return
	}
	var domains int
	var bytes int64
	for _, a := range accts {
		if a.Domain != "" {
			domains++
		}
		bytes += a.BytesTotal
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "kind": job.SourceKind, "panel": panelLabel(job.SourceKind),
		"accounts": len(accts), "domains": domains, "bytes": bytes})
}

func sshUserOrRoot(u string) string {
	u = strings.TrimSpace(u)
	if u == "" || u == "wp" {
		return "root"
	}
	return u
}

func panelLabel(kind string) string {
	switch kind {
	case models.MigrationSourceCpanel, models.MigrationSourceWHMpkgacct:
		return "cPanel/WHM"
	case models.MigrationSourceDirectAdmin:
		return "DirectAdmin"
	case models.MigrationSourceHestia:
		return "HestiaCP"
	case models.MigrationSourcePlesk:
		return "Plesk"
	}
	return kind
}

// describeAccount (GH #665) — the wizard's lazy per-account detail probe. Runs
// the Discoverer's DescribeAccount for ONE source account (on demand, so a 42-
// account box isn't fully probed up front) → returns per-account counts +
// warnings (PHP version, missing DNS zone, custom Apache includes, …).
func (h *adminMigrationsHandler) describeAccount(c *gin.Context) {
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	var req struct {
		SourceUser string `json:"source_user"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.SourceUser) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_user required"})
		return
	}
	d := panelDiscoverer(job.SourceKind, migrationAllowPrivate(h, c))
	if d == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_kind"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()
	secret := migrate.SecretRef{Path: filepath.Join(migrate.SecretsDir, job.ID+".env")}
	migrate.ApplyPort(d, job.SourcePort) // GH #429: test/describe previously ignored the custom SSH port (only the run path applied it)
	sess, err := d.Connect(ctx, job.SourceHost, sshUserOrRoot(job.SourceUser), secret)
	if err != nil {
		respondMigrateConnectErr(c, err)
		return
	}
	defer func() { _ = d.Close(ctx, sess) }()
	m, err := d.DescribeAccount(ctx, sess, strings.TrimSpace(req.SourceUser))
	if err != nil {
		respondAgentErr(c, "describe_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":        true,
		"databases": len(m.Databases),
		"mailboxes": len(m.Mailboxes),
		"domains":   len(m.Domains),
		"warnings":  m.Warnings,
	})
}

// migrationAllowPrivate reads the private-host toggle (shared by test-connection
// + describe-account).
func migrationAllowPrivate(h *adminMigrationsHandler, c *gin.Context) bool {
	if h.cfg.Settings == nil {
		return false
	}
	st, err := h.cfg.Settings.Get(c.Request.Context())
	if err != nil || st == nil {
		return false
	}
	return st.MigrationAllowPrivateHosts
}
