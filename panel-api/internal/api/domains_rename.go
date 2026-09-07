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
// may rename their own domain, an admin any. Experimental phase 1 — web-only,
// mail must be off, and the app's own internal config (e.g. a WordPress
// siteurl) is NOT rewritten; the UI warns about that. The heavy lifting (gate,
// tombstone the old name, rename the row, move + re-own the docroot, re-render)
// lives in the shared userops.RenameDomain so any future CLI reuses it.
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

	// *reconciler.Reconciler satisfies RenameReconciler, but pass it as a truly
	// nil interface when unwired so RenameDomain's nil check holds (a typed-nil
	// pointer in an interface is not == nil).
	var rec userops.RenameReconciler
	if h.cfg.Reconciler != nil {
		rec = h.cfg.Reconciler
	}

	if err := userops.RenameDomain(ctx, userops.Deps{
		Domains:         h.cfg.Domains,
		DomainTeardowns: h.cfg.DomainTeardowns,
		Users:           h.cfg.Users,
		Agent:           h.cfg.Agent,
		// Mailboxes arms the fail-closed mailbox gate (a rename would purge
		// retained Stalwart accounts on the old name). DNSZones + SSLCerts let
		// the rename re-key the zone + reissue the cert for the new name.
		Mailboxes: h.cfg.Mailboxes,
		DNSZones:  h.cfg.DNSZones,
		SSLCerts:  h.cfg.SSLCerts,
		Log:       slog.Default(),
	}, rec, domain, newName); err != nil {
		var re *userops.RenameError
		if errors.As(err, &re) {
			c.JSON(renameHTTPStatus(re.Code), gin.H{"error": re.Code, "message": re.Message})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": domain.ID, "name": domain.Name, "doc_root": domain.DocRoot})
}

// renameHTTPStatus maps a RenameError code to an HTTP status. Gate/validation
// reasons are 4xx; an infrastructure failure while carrying out the rename
// (moving files, or persisting the row) is 5xx and re-runnable — the file move
// and the row rename are ordered so a retry finishes cleanly.
func renameHTTPStatus(code string) int {
	switch code {
	case "invalid_name", "noop", "custom_docroot", "ambiguous_docroot":
		return http.StatusBadRequest
	case "mail_active", "mailboxes_present", "panel_primary", "web_disabled",
		"name_taken", "owner_unprovisioned", "owner_unresolved":
		return http.StatusConflict
	case "not_found":
		return http.StatusNotFound
	case "unavailable":
		return http.StatusServiceUnavailable
	default: // persist_failed, move_failed, lookup_failed
		return http.StatusInternalServerError
	}
}
