package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/composerrun"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainComposerHandlerConfig wires the Composer routes of a domain (GH #2276,
// ADR-0176).
type DomainComposerHandlerConfig struct {
	Domains  repository.DomainRepository
	Users    repository.UserRepository
	Packages repository.PackageRepository
	Runs     repository.ComposerRunRepository
	// Settings holds each domain's Composer project folder.
	Settings repository.DomainComposerSettingsRepository
	Agent    agent.AgentInterface
	// StartRateLimit bounds starting runs. Wire from rl.StrictPerActor(); nil
	// disables it.
	StartRateLimit gin.HandlerFunc
}

// RegisterDomainComposerRoutes adds the Composer endpoints of a domain:
//   - GET  /domains/:id/composer                      may the owner run Composer, where composer.json is, the run in progress
//   - GET  /domains/:id/composer/runs                 the domain's runs, newest first
//   - POST /domains/:id/composer/runs                 start a run
//   - GET  /domains/:id/composer/runs/:runId          one run, with its log
//   - POST /domains/:id/composer/runs/:runId/cancel   end a run in progress
//   - PUT  /domains/:id/composer/settings             set or clear the domain's project folder
//
// The domain's owner and admins may use them. Starting a run needs the
// owner's hosting package to allow Composer; a user with no package can't
// (GH #282: a privileged feature).
func RegisterDomainComposerRoutes(g *gin.RouterGroup, cfg DomainComposerHandlerConfig) {
	if cfg.Domains == nil || cfg.Users == nil || cfg.Packages == nil || cfg.Runs == nil || cfg.Settings == nil || cfg.Agent == nil {
		panic("api.RegisterDomainComposerRoutes: Domains, Users, Packages, Runs, Settings and Agent are required")
	}
	h := &domainComposerHandler{cfg: cfg}
	startLimit := cfg.StartRateLimit
	if startLimit == nil {
		startLimit = func(c *gin.Context) { c.Next() }
	}
	g.GET("/domains/:id/composer", h.get)
	g.GET("/domains/:id/composer/runs", h.listRuns)
	g.POST("/domains/:id/composer/runs", startLimit, h.startRun)
	g.GET("/domains/:id/composer/runs/:runId", h.getRun)
	g.POST("/domains/:id/composer/runs/:runId/cancel", h.cancelRun)
	g.PUT("/domains/:id/composer/settings", h.putSettings)
}

type domainComposerHandler struct {
	cfg DomainComposerHandlerConfig
}

// composerRunDTO is a run on the wire: its packages as a list, and its log
// when one run is read.
type composerRunDTO struct {
	models.ComposerRun
	Packages []string `json:"packages"`
	Log      string   `json:"log,omitempty"`
}

func newComposerRunDTO(run *models.ComposerRun, withLog bool) composerRunDTO {
	d := composerRunDTO{ComposerRun: *run, Packages: run.PackageList()}
	if withLog {
		d.Log = run.Log
	}
	return d
}

// composerOwner is the domain a request is about and the user who owns it.
type composerOwner struct {
	domain *models.Domain
	user   *models.User
}

// loadDomain loads the domain in the path and its owner, answering the
// request itself when the caller may not see the domain.
func (h *domainComposerHandler) loadDomain(c *gin.Context) (*composerOwner, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil, false
	}
	ctx := c.Request.Context()
	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return nil, false
	}
	if err != nil {
		slog.ErrorContext(ctx, "composer: load domain", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, false
	}
	owner, err := h.cfg.Users.FindByID(ctx, dom.UserID)
	if err != nil || owner == nil {
		slog.ErrorContext(ctx, "composer: load the domain's owner", "domain", dom.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	return &composerOwner{domain: dom, user: owner}, true
}

// composerAllowed says whether the owner may run Composer, and if not, why:
// the error code the API answers with.
func (h *domainComposerHandler) composerAllowed(c *gin.Context, o *composerOwner) (bool, string) {
	if o.user.Username == nil || *o.user.Username == "" {
		return false, "no_account"
	}
	if o.user.Suspended {
		return false, "account_suspended"
	}
	if o.user.PackageID == nil {
		return false, "composer_requires_package"
	}
	pkg, err := h.cfg.Packages.FindByID(c.Request.Context(), *o.user.PackageID)
	if err != nil || pkg == nil {
		return false, "composer_requires_package"
	}
	if !pkg.ComposerEnabled {
		return false, "composer_not_in_package"
	}
	return true, ""
}

// projectDir is the domain's Composer project folder, nil when it has none
// and finds composer.json on its own. It answers the request itself when the
// settings can't be read.
func (h *domainComposerHandler) projectDir(c *gin.Context, o *composerOwner) (*string, bool) {
	s, err := h.cfg.Settings.Get(c.Request.Context(), o.domain.ID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, true
	}
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "composer: load the domain's settings", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	return &s.ProjectDir, true
}

// refresh brings a running run up to date from the agent. When the agent
// can't be asked, the run is answered as stored.
func (h *domainComposerHandler) refresh(c *gin.Context, run *models.ComposerRun) {
	if err := composerrun.Refresh(c.Request.Context(), h.cfg.Agent, h.cfg.Runs, run); err != nil {
		slog.WarnContext(c.Request.Context(), "composer: refresh a run", "run", run.ID, "error", err)
	}
}

type composerStatusResponse struct {
	// Enabled: the owner's hosting package allows Composer runs.
	Enabled bool `json:"enabled"`
	// Reason is why not: no_account, account_suspended,
	// composer_requires_package or composer_not_in_package.
	Reason string `json:"reason,omitempty"`
	// ProjectDir is the domain's project folder, relative to the home; null
	// when the domain finds composer.json on its own.
	ProjectDir *string `json:"project_dir"`
	// Detect is where the domain's composer.json files are, from the docroot
	// up to the domain's directory, or what its project folder holds; absent
	// when Composer is not enabled or the server could not be asked.
	Detect    *composerrun.DetectResponse `json:"detect,omitempty"`
	ActiveRun *composerRunDTO             `json:"active_run"`
}

func (h *domainComposerHandler) get(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	resp := composerStatusResponse{}
	if resp.ProjectDir, ok = h.projectDir(c, o); !ok {
		return
	}
	resp.Enabled, resp.Reason = h.composerAllowed(c, o)
	if resp.Enabled {
		det, err := composerrun.Detect(ctx, h.cfg.Agent, composerrun.DetectRequest{
			OSUser: *o.user.Username, Domain: o.domain.Name, Docroot: o.domain.DocRoot,
			ProjectDir: resp.ProjectDir,
		})
		if err != nil {
			slog.WarnContext(ctx, "composer: detect", "domain", o.domain.ID, "error", err)
		} else {
			resp.Detect = det
		}
	}
	runs, _, err := h.cfg.Runs.ListByDomain(ctx, o.domain.ID, 0, 1)
	if err != nil {
		slog.ErrorContext(ctx, "composer: list runs", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if len(runs) == 1 && runs[0].State == models.ComposerRunRunning {
		h.refresh(c, &runs[0])
		if runs[0].State == models.ComposerRunRunning {
			d := newComposerRunDTO(&runs[0], false)
			resp.ActiveRun = &d
		}
	}
	c.JSON(http.StatusOK, resp)
}

func (h *domainComposerHandler) listRuns(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	page, pageSize := parsePagination(c)
	runs, total, err := h.cfg.Runs.ListByDomain(c.Request.Context(), o.domain.ID, (page-1)*pageSize, pageSize)
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "composer: list runs", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	data := make([]composerRunDTO, 0, len(runs))
	for i := range runs {
		h.refresh(c, &runs[i])
		data = append(data, newComposerRunDTO(&runs[i], false))
	}
	c.JSON(http.StatusOK, gin.H{"data": data, "total": total, "page": page, "page_size": pageSize})
}

type startComposerRunRequest struct {
	Command  string   `json:"command"`
	AppDir   string   `json:"app_dir"`
	Packages []string `json:"packages"`
	NoDev    bool     `json:"no_dev"`
}

func (h *domainComposerHandler) startRun(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	if allowed, reason := h.composerAllowed(c, o); !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": reason})
		return
	}
	var req startComposerRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json", "detail": err.Error()})
		return
	}
	if err := composerrun.Validate(req.Command, req.Packages, req.NoDev, req.AppDir); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_run", "detail": err.Error()})
		return
	}
	projectDir, ok := h.projectDir(c, o)
	if !ok {
		return
	}
	workDir := composerrun.DomainWorkDir(o.domain.Name, req.AppDir)
	if projectDir != nil {
		if req.AppDir != "" {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_run", "detail": "this domain runs Composer in its project folder " + *projectDir + "/: leave app_dir empty"})
			return
		}
		workDir = *projectDir
	}
	now := time.Now().UTC()
	run := &models.ComposerRun{
		ID: ids.NewULID(), UserID: o.domain.UserID, DomainID: o.domain.ID,
		AppDir: req.AppDir, WorkDir: workDir, Command: req.Command, NoDev: req.NoDev,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	run.SetPackageList(req.Packages)

	err := composerrun.Start(c.Request.Context(), h.cfg.Agent, h.cfg.Runs, run, *o.user.Username, o.domain.Name)
	switch {
	case err == nil:
		c.JSON(http.StatusAccepted, newComposerRunDTO(run, false))
	case errors.Is(err, repository.ErrComposerRunActive):
		c.JSON(http.StatusConflict, gin.H{"error": "composer_run_active", "detail": "a Composer run is already in progress for this domain or in " + workDir + "/"})
	case errors.Is(err, repository.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
	default:
		if ae, refused := composerrun.Refusal(err); refused {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": ae.Code, "detail": ae.Message, "run": newComposerRunDTO(run, false)})
			return
		}
		logAgentError(c, "composer_start_failed", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "composer_start_failed", "detail": "the server could not start the run", "run": newComposerRunDTO(run, false)})
	}
}

// loadRun loads the run in the path, which must belong to the domain.
func (h *domainComposerHandler) loadRun(c *gin.Context, o *composerOwner) (*models.ComposerRun, bool) {
	run, err := h.cfg.Runs.FindByID(c.Request.Context(), c.Param("runId"))
	if errors.Is(err, repository.ErrNotFound) || (err == nil && run.DomainID != o.domain.ID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "run_not_found"})
		return nil, false
	}
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "composer: load run", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	return run, true
}

func (h *domainComposerHandler) getRun(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	run, ok := h.loadRun(c, o)
	if !ok {
		return
	}
	h.refresh(c, run)
	c.JSON(http.StatusOK, newComposerRunDTO(run, true))
}

func (h *domainComposerHandler) cancelRun(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	run, ok := h.loadRun(c, o)
	if !ok {
		return
	}
	if run.State != models.ComposerRunRunning {
		c.JSON(http.StatusConflict, gin.H{"error": "composer_run_finished", "run": newComposerRunDTO(run, true)})
		return
	}
	err := composerrun.Cancel(c.Request.Context(), h.cfg.Agent, h.cfg.Runs, run)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, newComposerRunDTO(run, true))
	case errors.Is(err, composerrun.ErrNotRunning):
		c.JSON(http.StatusConflict, gin.H{"error": "composer_run_finished", "run": newComposerRunDTO(run, true)})
	default:
		logAgentError(c, "composer_cancel_failed", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "composer_cancel_failed", "detail": "the server could not stop the run"})
	}
}

type putComposerSettingsRequest struct {
	// ProjectDir is the folder to run Composer in, relative to the home:
	// "domains" or a folder inside it. null clears it, so the domain finds
	// composer.json on its own again.
	ProjectDir *string `json:"project_dir"`
}

// putSettings sets or clears the domain's project folder. Runs already
// started keep the folder they started in.
func (h *domainComposerHandler) putSettings(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	if allowed, reason := h.composerAllowed(c, o); !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": reason})
		return
	}
	var req putComposerSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json", "detail": err.Error()})
		return
	}
	ctx := c.Request.Context()
	if req.ProjectDir == nil {
		if err := h.cfg.Settings.Delete(ctx, o.domain.ID); err != nil {
			slog.ErrorContext(ctx, "composer: clear the domain's settings", "domain", o.domain.ID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"project_dir": nil})
		return
	}
	dir := strings.TrimSuffix(*req.ProjectDir, "/")
	if err := composerrun.ValidateProjectDir(dir); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_project_dir", "detail": err.Error()})
		return
	}
	if err := h.cfg.Settings.Put(ctx, &models.DomainComposerSettings{DomainID: o.domain.ID, ProjectDir: dir}); err != nil {
		slog.ErrorContext(ctx, "composer: store the domain's settings", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"project_dir": dir})
}
