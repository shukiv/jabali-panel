// Per-domain directory tree browser. Powers the "Pick a folder" modal
// in DomainDirectoryPrivacySection so tenants don't type a path by hand.
//
// Route: GET /api/v1/domains/:id/browse?path=<rel>
//   - rel is RELATIVE to the domain's docroot; leading "/" means docroot
//     root, "/wp-admin" means <docroot>/wp-admin.
//   - Returns directory entries only (no files); the picker shows folders.
//
// Auth: admin OR domain owner. Cross-tenant access 404s the domain (same
// shape as the rest of the domains family).
package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type DomainPathBrowseConfig struct {
	Domains repository.DomainRepository
	Users   repository.UserRepository
	Agent   agent.AgentInterface
}

func RegisterDomainPathBrowseRoutes(g *gin.RouterGroup, cfg DomainPathBrowseConfig) {
	if cfg.Domains == nil || cfg.Users == nil || cfg.Agent == nil {
		return
	}
	h := &domainPathBrowseHandler{cfg: cfg}
	g.GET("/domains/:id/browse", h.browse)
}

type domainPathBrowseHandler struct {
	cfg DomainPathBrowseConfig
}

type domainBrowseEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

type domainBrowseResult struct {
	DocRoot string              `json:"doc_root"`
	Path string `json:"path"` // canonical relative path (no trailing /)
	// NOTE: the server-side absolute path is deliberately NOT returned. It
	// leaks /home/<linux-user>/..., i.e. the tenant's Linux username, which
	// is useful pre-recon for SSH/password attacks against that account.
	// Auth and containment here are correct; this was pure over-disclosure.
	// It stays available server-side for logs.
	Entries []domainBrowseEntry `json:"entries"`
}

func (h *domainPathBrowseHandler) browse(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	if dom.DocRoot == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain has no doc_root"})
		return
	}
	rel := normaliseBrowseRel(c.Query("path"))
	if rel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
		return
	}
	abs := filepath.Join(dom.DocRoot, strings.TrimPrefix(rel, "/"))
	// Defence-in-depth: refuse to leave docroot even if normalisation
	// missed something pathological (e.g. unicode-encoded ..).
	rel2, err := filepath.Rel(dom.DocRoot, abs)
	if err != nil || strings.HasPrefix(rel2, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path escapes docroot"})
		return
	}

	// Resolve domain owner's linux username — files.list runs as that user.
	owner, err := h.cfg.Users.FindByID(c.Request.Context(), dom.UserID)
	if err != nil || owner == nil || owner.Username == nil || *owner.Username == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "domain owner has no linux account"})
		return
	}
	username := *owner.Username

	raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.list", map[string]any{
		"user_id":  dom.UserID,
		"username": username,
		"path":     abs,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "list failed", "detail": err.Error()})
		return
	}
	var agentResp struct {
		Path    string `json:"path"`
		Entries []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"is_dir"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &agentResp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad agent response"})
		return
	}
	out := domainBrowseResult{
		DocRoot: dom.DocRoot,
		Path:    rel,
		Entries: make([]domainBrowseEntry, 0, len(agentResp.Entries)),
	}
	for _, e := range agentResp.Entries {
		if !e.IsDir {
			continue
		}
		out.Entries = append(out.Entries, domainBrowseEntry{Name: e.Name, IsDir: true})
	}
	c.JSON(http.StatusOK, out)
}

func (h *domainPathBrowseHandler) resolveDomain(c *gin.Context) (*models.Domain, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return nil, false
	}
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return nil, false
	}
	return dom, true
}

// normaliseBrowseRel coerces a user-supplied path into a safe docroot-
// relative form: starts with /, no //, no .. segments, only the
// privacy-safe charset. Returns "" if the input can't be made safe.
func normaliseBrowseRel(in string) string {
	p := strings.TrimSpace(in)
	if p == "" || p == "/" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if strings.Contains(p, "//") {
		return ""
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return ""
		}
	}
	// Restrict charset to match the Directory Privacy path validator
	// (server-side defence in depth even though the browser is read-only).
	for _, r := range p {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '/' || r == '.' || r == '_' || r == '-':
		default:
			return ""
		}
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}
