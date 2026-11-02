// Paths of a domain that skip the AppSec bot-detection challenge (GH #2328).
//
// Routes (mounted under /api/v1/domains/:id/bot-challenge-exclusions):
//
//	GET              list the domain's paths
//	POST             add a path
//	DELETE /:excl_id remove one
//
// Admin-only, like the domain's bot_challenge_exempt switch: an exclusion
// lowers protection on a shared server. The reconciler writes the paths to
// the agent's state file on its next pass (within a minute); the WAF, rate
// limits and CrowdSec IP decisions still apply to those requests.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// maxBotChallengeExclusionNoteLen is the note column (varchar(512)).
const maxBotChallengeExclusionNoteLen = 512

// BotChallengeExclusionHandlerConfig wires the routes. Both repositories are
// required; nil disables route registration.
type BotChallengeExclusionHandlerConfig struct {
	Domains    repository.DomainRepository
	Exclusions repository.BotChallengeExclusionRepository
}

func RegisterBotChallengeExclusionRoutes(g *gin.RouterGroup, cfg BotChallengeExclusionHandlerConfig) {
	if cfg.Domains == nil || cfg.Exclusions == nil {
		return
	}
	h := &botChallengeExclusionHandler{cfg: cfg}
	rg := g.Group("/domains/:id/bot-challenge-exclusions", middleware.RequireAdmin())
	rg.GET("", h.list)
	rg.POST("", h.create)
	rg.DELETE("/:excl_id", h.delete)
}

type botChallengeExclusionHandler struct {
	cfg BotChallengeExclusionHandlerConfig
}

// BotChallengeExclusionView is the wire shape of a row: methods as a list,
// empty for every method.
type BotChallengeExclusionView struct {
	ID         string    `json:"id"`
	DomainID   string    `json:"domain_id"`
	PathPrefix string    `json:"path_prefix"`
	Methods    []string  `json:"methods"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
}

// NewBotChallengeExclusionView converts a row for the API and the CLI.
func NewBotChallengeExclusionView(e models.BotChallengeExclusion) BotChallengeExclusionView {
	methods := e.MethodList()
	if methods == nil {
		methods = []string{}
	}
	return BotChallengeExclusionView{
		ID:         e.ID,
		DomainID:   e.DomainID,
		PathPrefix: e.PathPrefix,
		Methods:    methods,
		Note:       e.Note,
		CreatedAt:  e.CreatedAt,
	}
}

// BuildBotChallengeExclusion validates the input of a new row and returns it
// with a fresh id. Shared by the API and `jabali domain bot-exclusion add`.
func BuildBotChallengeExclusion(domainID, pathPrefix string, methods []string, note string) (*models.BotChallengeExclusion, error) {
	pathPrefix = strings.TrimSpace(pathPrefix)
	if err := appseccfg.ValidateBotPathPrefix(pathPrefix); err != nil {
		return nil, err
	}
	normalized, err := appseccfg.NormalizeBotMethods(methods)
	if err != nil {
		return nil, err
	}
	note = strings.TrimSpace(note)
	if len(note) > maxBotChallengeExclusionNoteLen {
		return nil, fmt.Errorf("note is longer than %d characters", maxBotChallengeExclusionNoteLen)
	}
	if strings.IndexFunc(note, unicode.IsControl) >= 0 {
		return nil, errors.New("note contains a line break or control character")
	}
	return &models.BotChallengeExclusion{
		ID:         ids.NewULID(),
		DomainID:   domainID,
		PathPrefix: pathPrefix,
		Methods:    strings.Join(normalized, ","),
		Note:       note,
	}, nil
}

func (h *botChallengeExclusionHandler) list(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	rows, err := h.cfg.Exclusions.ListByDomain(c.Request.Context(), dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	views := make([]BotChallengeExclusionView, 0, len(rows))
	for _, r := range rows {
		views = append(views, NewBotChallengeExclusionView(r))
	}
	c.JSON(http.StatusOK, gin.H{"data": views, "total": len(views)})
}

func (h *botChallengeExclusionHandler) create(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	var req struct {
		PathPrefix string   `json:"path_prefix"`
		Methods    []string `json:"methods"`
		Note       string   `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}
	row, err := BuildBotChallengeExclusion(dom.ID, req.PathPrefix, req.Methods, req.Note)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx := c.Request.Context()
	n, err := h.cfg.Exclusions.CountByDomain(ctx, dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if n >= appseccfg.MaxBotPathExclusionsPerDomain {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("a domain can list at most %d paths", appseccfg.MaxBotPathExclusionsPerDomain)})
		return
	}
	if err := h.cfg.Exclusions.Create(ctx, row); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "this path is already listed for these methods"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusCreated, NewBotChallengeExclusionView(*row))
}

func (h *botChallengeExclusionHandler) delete(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	id := c.Param("excl_id")
	if err := h.cfg.Exclusions.DeleteForDomain(c.Request.Context(), dom.ID, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "exclusion_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// resolveDomain looks the domain up by the URL :id and writes the HTTP error
// when it is not there. The group's RequireAdmin already ran.
func (h *botChallengeExclusionHandler) resolveDomain(c *gin.Context) (*models.Domain, bool) {
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	return dom, true
}
