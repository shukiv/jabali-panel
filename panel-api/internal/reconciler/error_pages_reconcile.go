package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcileErrorPages converges the shared branded pages from the editable
// page_template rows onto disk: the three error pages (404/403/500) into
// /var/www/jabali-errors, the disabled-site page into
// /var/www/jabali-disabled, and the unconfigured-domain page into
// /var/www/jabali-unconfigured — plus the default-vhost catch-all mode
// (GH #860: serve that page vs the shipped `return 444` drop, from
// server_settings.unconfigured_page_enabled). The bodies are global (no
// per-domain placeholders), so one set serves all vhosts. Hash-gated: it
// only calls the agent when content or the toggle actually changed, so the
// steady-state tick is a pure noop. On a failed sync it does NOT cache the
// hash, so the next tick retries.
func (r *Reconciler) reconcileErrorPages(ctx context.Context) {
	if r.pageTemplates == nil || r.agent == nil {
		return
	}
	body := func(key string) string {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if row, err := r.pageTemplates.Get(c, key); err == nil && row != nil && row.Content != "" {
			return row.Content
		}
		// Row missing/empty — fall back to the compiled default so the
		// files always exist for nginx's error_page to find.
		return repository.DefaultPageTemplateBody(key)
	}
	e404 := body(models.PageTemplateError404)
	e403 := body(models.PageTemplateError403)
	e500 := body(models.PageTemplateError500)
	disabled := body(models.PageTemplateDomainDisabled)
	unconfigured := body(models.PageTemplateDomainUnconfigured)

	unconfiguredOn := false
	if r.serverSettings != nil {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		if s, err := r.serverSettings.Get(c); err == nil && s != nil {
			unconfiguredOn = s.UnconfiguredPageEnabled
		}
		cancel()
	}

	sum := sha256.Sum256([]byte(e404 + "\x00" + e403 + "\x00" + e500 +
		"\x00" + disabled + "\x00" + unconfigured + "\x00" + strconv.FormatBool(unconfiguredOn)))
	hash := hex.EncodeToString(sum[:])
	if hash == r.errorPagesHash {
		return
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := r.agent.Call(cctx, "page_templates.sync_error_pages", map[string]any{
		"error_404":            e404,
		"error_403":            e403,
		"error_500":            e500,
		"domain_disabled":      disabled,
		"domain_unconfigured":  unconfigured,
		"unconfigured_enabled": unconfiguredOn,
	}); err != nil {
		r.log.Warn("error-pages sync failed", "error", err)
		return
	}
	r.errorPagesHash = hash
	r.log.Debug("branded pages synced (errors/disabled/unconfigured + catch-all mode)")
}
