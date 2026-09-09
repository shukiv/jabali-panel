package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

type renameDomainRequest struct {
	Name string `json:"name" binding:"required"`
}

// rename renames an existing domain in place (GH #1579). Owner-scoped: a tenant
// may rename their own domain, an admin any. Experimental. Mail is CARRIED to the
// new name (the Stalwart registry domain is renamed in place, so mailboxes,
// stored messages, and DKIM follow) and the per-domain mail cert is re-queued for
// mail.<new>. A WordPress install's stored site URL IS rewritten to the new name
// (best-effort); any install that could not be rewritten comes back in the
// response `warnings` (surfaced by the UI). Other apps' internal config is
// unchanged. The heavy lifting (gate, tombstone the old name, rename the row,
// move + re-own the docroot, carry mail, rewrite app URLs, re-render) lives in
// the shared userops.RenameDomain so any future CLI reuses it.
func (h *domainHandler) rename(c *gin.Context) {
	ctx := c.Request.Context()
	domain, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	var req renameDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	// Same normalize + RFC-shape validation the create source of truth runs, so
	// a rename can never produce a name create would have rejected.
	newName := normalizeDomainName(req.Name)
	if verr := validateDomainName(newName); verr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name", "message": verr.Error()})
		return
	}
	// GH #1625: a rename assigns a new domains.name too, so it must run the same
	// cross-tenant hijack guard create does — the new name must not claim a
	// server_name already held by another domain's web-domain alias. (The
	// domain's own aliases survive the rename; they are keyed by domain_id.)
	if hit, clash := aliasCollision(ctx, h.cfg.WebDomainAliases, newName); clash {
		c.JSON(http.StatusConflict, gin.H{"error": "domain_conflicts_alias", "message": "the name " + hit + " is already used as an alias of another domain"})
		return
	}

	// *reconciler.Reconciler satisfies RenameReconciler, but pass it as a truly
	// nil interface when unwired so RenameDomain's nil check holds (a typed-nil
	// pointer in an interface is not == nil).
	var rec userops.RenameReconciler
	if h.cfg.Reconciler != nil {
		rec = h.cfg.Reconciler
	}

	warnings, err := userops.RenameDomain(ctx, userops.Deps{
		Domains:         h.cfg.Domains,
		DomainTeardowns: h.cfg.DomainTeardowns,
		Users:           h.cfg.Users,
		Agent:           h.cfg.Agent,
		// DNSZones + SSLCerts let the rename re-key the zone + reissue the web
		// cert for the new name; MailCerts re-queues the per-domain mail cert for
		// mail.<new>; AppInstalls lets it rewrite a WordPress install's stored site
		// URL. Mail data is carried by the mail.domain.rename agent verb (via
		// h.cfg.Agent), so no mailbox repo is needed here.
		DNSZones:    h.cfg.DNSZones,
		SSLCerts:    h.cfg.SSLCerts,
		MailCerts:   h.cfg.MailCerts,
		AppInstalls: h.cfg.AppInstalls,
		// FtpAccounts backs the refusal when an FTP/SFTP subaccount is homed
		// under the docroot being moved (its jail/chroot is not moved here).
		FtpAccounts: h.cfg.FtpAccounts,
		// DMARC / TLSRPT move the domain's aggregate report history onto the new
		// name so those dashboards are not orphaned.
		DMARC:  h.cfg.DMARCAggregate,
		TLSRPT: h.cfg.TLSRPTAggregate,
		// Forwarders rewrites alias forwarder targets to the new name.
		Forwarders: h.cfg.Forwarders,
		// Settings gates the mail-carry verb on ServerSettings.MailEnabled so a
		// rename on a server without the mail module installed skips the Stalwart
		// call instead of failing on the absent admin token (GH #1579).
		Settings: h.cfg.ServerSettings,
		Log:      slog.Default(),
	}, rec, domain, newName)
	if err != nil {
		var re *userops.RenameError
		if errors.As(err, &re) {
			c.JSON(renameHTTPStatus(re.Code), gin.H{"error": re.Code, "message": re.Message})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// warnings carries any best-effort app-URL rewrite that did not complete —
	// the rename itself succeeded. Always an array (never null) so the client
	// can render it uniformly.
	if warnings == nil {
		warnings = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"id": domain.ID, "name": domain.Name, "doc_root": domain.DocRoot, "warnings": warnings})
}

// renameHTTPStatus maps a RenameError code to an HTTP status. Gate/validation
// reasons are 4xx; an infrastructure failure while carrying out the rename
// (moving files, or persisting the row) is 5xx and re-runnable — the file move
// and the row rename are ordered so a retry finishes cleanly.
func renameHTTPStatus(code string) int {
	switch code {
	case "invalid_name", "noop", "custom_docroot", "ambiguous_docroot":
		return http.StatusBadRequest
	case "mail_domain_conflict", "panel_primary", "web_disabled",
		"ssl_custom_cert", "name_taken", "owner_unprovisioned", "owner_unresolved",
		"ftp_subaccounts":
		return http.StatusConflict
	case "not_found":
		return http.StatusNotFound
	case "unavailable", "ftp_check_failed":
		return http.StatusServiceUnavailable
	default: // persist_failed, move_failed, lookup_failed
		return http.StatusInternalServerError
	}
}
