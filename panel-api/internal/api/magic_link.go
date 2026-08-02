// Package api: M22 admin-login mint endpoint.
//
// Per ADR-0040 (M22 rework — self-deleting sso file), the mint endpoint
// dispatches to the agent's wordpress.create_sso_file command, which
// writes a self-deleting jabali-sso-<nonce>.php into the install's
// webroot. The panel returns the URL the operator opens. There is no
// validate endpoint anymore (the PHP file is its own validator) and no
// signing key (the 256-bit nonce in the filename is the capability).
//
// Wire contract preserved from ADR-0039: POST /api/v1/applications/:id/magic-link
// returns {url, expires_in}. Only the URL shape changes —
//   was:  https://<site>/?jabali_admin_login=<token>
//   now:  https://<site>[/<subdir>]/jabali-sso-<43chars>.php

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// MagicLinkHandlerConfig wires the dependencies the mint endpoint needs.
//
// (As of M22 rework / ADR-0040 there is only the mint endpoint. The
// validate endpoint, the magic_link_tokens repository, the HMAC signing
// keys, and the clock-skew tolerance are all gone.)
type MagicLinkHandlerConfig struct {
	ApplicationInstalls repository.ApplicationInstallRepository
	Domains             repository.DomainRepository
	Users               repository.UserRepository
	Agent               agent.AgentInterface
}

// ssoTTLSeconds mirrors the agent-side constant (panel-agent
// commands.ssoTTLSeconds). Returned to the SPA in the mint response so
// the UI can show a progress hint. The two must agree; if you bump one
// without the other the SPA's countdown drifts vs the actual lifetime.
const ssoTTLSeconds = 60

// ssoAgentCommandFor maps an application_installs.app_type value to the
// agent command that writes the self-deleting SSO PHP file for that
// CMS. Returns false for app_types that don't yet have an SSO-file
// implementation — the mint handler maps that to 409
// unsupported_app_type.
//
// Adding a new CMS to magic-link is three steps:
//  1. Add a `<cms>.create_sso_file` handler in panel-agent with its
//     per-CMS PHP template.
//  2. Add the mapping here.
//  3. Widen the panel-ui "Log in to admin" button's app_type filter.
func ssoAgentCommandFor(appType string) (string, bool) {
	switch appType {
	case "wordpress":
		return "wordpress.create_sso_file", true
	case "drupal":
		return "drupal.create_sso_file", true
	case "joomla":
		return "joomla.create_sso_file", true
	default:
		return "", false
	}
}

// RegisterMagicLinkRoutes mounts POST /applications/:id/magic-link under
// the v1 group where the Kratos session middleware already applies.
//
// Pre-rework this function took a *gin.Engine for the unauthenticated
// validate endpoint mounted on the root. The validate endpoint is gone;
// the second arg is gone with it.
// SSOAgentCommandFor / ComposeInstallPath / ComposeSSOURL / SSOTTLSeconds are
// exported for the `jabali app magic-link` CLI (#573) so it mints against the
// SAME supported-app map, path composition, URL shape, and TTL as this endpoint
// — no drift.
func SSOAgentCommandFor(appType string) (string, bool) { return ssoAgentCommandFor(appType) }
func ComposeInstallPath(docRoot, subdirectory string) string {
	return composeInstallPath(docRoot, subdirectory)
}
func ComposeSSOURL(domainName, subdirectory, fileName string) string {
	return composeSSOURL(domainName, subdirectory, fileName)
}

// SSOTTLSeconds is the magic-link lifetime in seconds (ADR-0040).
const SSOTTLSeconds = ssoTTLSeconds

func RegisterMagicLinkRoutes(v1 *gin.RouterGroup, cfg MagicLinkHandlerConfig) {
	h := &magicLinkHandlers{cfg: cfg}
	v1.POST("/applications/:id/magic-link", h.mint)
}

type magicLinkHandlers struct {
	cfg MagicLinkHandlerConfig
}

// mintResponse is what the panel UI gets back. The URL is ready to
// open in a new tab; expires_in is for an SPA progress hint.
type mintResponse struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
}

// agentCreateSSOFileResp mirrors the agent's createSSOFileResp wire
// shape (panel-agent commands.createSSOFileResp). We unmarshal into a
// local copy rather than importing the agent package to keep the panel
// independent of the agent's internal types.
type agentCreateSSOFileResp struct {
	FileName      string `json:"file_name"`
	ExpiresAtUnix int64  `json:"expires_at_unix"`
}

func (h *magicLinkHandlers) mint(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	installID := c.Param("id")

	install, err := h.loadInstall(ctx, installID, claims.UserID, claims.IsAdmin)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Don't leak existence on cross-tenant attempts.
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "install not found"})
			return
		}
		slog.ErrorContext(ctx, "magiclink mint: install lookup failed", "err", err, "install_id", installID)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	if install.Status != "ready" {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error":  "install_not_ready",
			"detail": "install is in status " + install.Status,
		})
		return
	}
	agentCommand, ok := ssoAgentCommandFor(install.AppType)
	if !ok {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error":  "unsupported_app_type",
			"detail": "magic-link login not implemented for app_type " + install.AppType,
		})
		return
	}

	domain, err := h.cfg.Domains.FindByID(ctx, install.DomainID)
	if err != nil {
		slog.ErrorContext(ctx, "magiclink mint: domain lookup failed", "err", err, "install_id", installID, "domain_id", install.DomainID)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "domain lookup failed"})
		return
	}
	if strings.TrimSpace(domain.DocRoot) == "" {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "domain has no docroot"})
		return
	}

	osUser, err := h.resolveOSUser(ctx, install.UserID)
	if err != nil {
		slog.ErrorContext(ctx, "magiclink mint: os_user resolve failed", "err", err, "install_id", installID, "user_id", install.UserID)
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "user_not_provisioned"})
		return
	}
	if install.AdminUsername == "" {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "admin_user_unresolved"})
		return
	}

	installPath := composeInstallPath(domain.DocRoot, install.Subdirectory)
	payload := map[string]any{
		"install_path":   installPath,
		"os_user":        osUser,
		"install_id":     install.ID,
		"admin_username": install.AdminUsername,
	}

	if h.cfg.Agent == nil {
		slog.ErrorContext(ctx, "magiclink mint: agent not configured")
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "agent not configured"})
		return
	}
	raw, err := h.cfg.Agent.Call(ctx, agentCommand, payload)
	if err != nil {
		slog.ErrorContext(ctx, "magiclink mint: agent call failed", "err", err, "install_id", installID)
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "agent_failed"})
		return
	}
	var agentResp agentCreateSSOFileResp
	if err := json.Unmarshal(raw, &agentResp); err != nil {
		slog.ErrorContext(ctx, "magiclink mint: agent response decode failed", "err", err, "install_id", installID)
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "agent_response_invalid"})
		return
	}
	if agentResp.FileName == "" {
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "agent_response_invalid"})
		return
	}

	url := composeSSOURL(domain.Name, install.Subdirectory, agentResp.FileName)

	// Audit log without the full file name — log a short SHA prefix
	// so operators can correlate panel + agent + WP error_log entries
	// without ever writing a working credential into the panel logs.
	hash := sha256.Sum256([]byte(agentResp.FileName))
	hashPrefix := hex.EncodeToString(hash[:6])
	slog.InfoContext(ctx, "magiclink mint",
		"operator", claims.UserID,
		"install_id", install.ID,
		"file_hash_prefix", hashPrefix,
		"expires_at_unix", agentResp.ExpiresAtUnix,
	)

	c.JSON(http.StatusOK, mintResponse{
		URL:       url,
		ExpiresIn: ssoTTLSeconds,
	})
}

// loadInstall returns the install row, scoped by ownership unless the
// caller is admin. ErrNotFound on cross-tenant attempts.
func (h *magicLinkHandlers) loadInstall(ctx context.Context, installID, userID string, isAdmin bool) (*models.ApplicationInstall, error) {
	if isAdmin {
		return h.cfg.ApplicationInstalls.FindByID(ctx, installID)
	}
	return h.cfg.ApplicationInstalls.FindByIDAndUserID(ctx, installID, userID)
}

// resolveOSUser looks up the install owner's linux username — required
// for the agent's chown step. Mirrors the pattern in wordpress.go's
// install/delete handlers.
func (h *magicLinkHandlers) resolveOSUser(ctx context.Context, panelUserID string) (string, error) {
	u, err := h.cfg.Users.FindByID(ctx, panelUserID)
	if err != nil {
		return "", err
	}
	if u == nil || u.Username == nil || *u.Username == "" {
		return "", fmt.Errorf("user %s has no linux username", panelUserID)
	}
	return *u.Username, nil
}

// composeInstallPath produces the agent-side install directory by
// joining the domain's docroot with the install's subdirectory (when
// present). path.Clean defends against accidental "//" or trailing-/
// shapes; the subdirectory column is operator-supplied and validated at
// install time but defence in depth is cheap.
func composeInstallPath(docRoot, subdirectory string) string {
	subdirectory = strings.Trim(subdirectory, "/")
	if subdirectory == "" {
		return docRoot
	}
	return path.Clean(docRoot + "/" + subdirectory)
}

// composeSSOURL builds the URL the operator opens. Subdirectory installs
// land at /<subdir>/<file>; root installs at /<file>.
func composeSSOURL(domainName, subdirectory, fileName string) string {
	subdirectory = strings.Trim(subdirectory, "/")
	if subdirectory == "" {
		return "https://" + domainName + "/" + fileName
	}
	return "https://" + domainName + "/" + subdirectory + "/" + fileName
}
