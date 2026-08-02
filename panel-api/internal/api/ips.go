package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// IPHandlerConfig wires the dependencies the M24 IP-pool endpoints need.
//
// AgentIPCommandsEnabled is the M24_AGENT_IP_COMMANDS feature flag. While
// false (Step 2), POST/DELETE only touch the DB and return — the agent
// kernel-binding flow (Step 3) is wired in by Step 4 once both halves
// have landed.
type IPHandlerConfig struct {
	Repo                   repository.ManagedIPRepository
	Domains                repository.DomainRepository
	Agent                  agent.AgentInterface
	AgentIPCommandsEnabled bool
	Log                    *slog.Logger
}

// RegisterIPRoutes mounts the M24 IP-pool surface:
//
//   - GET    /admin/ips             — list pool
//   - POST   /admin/ips             — add IP (admin)
//   - PATCH  /admin/ips/:id         — edit label/is_user_selectable/is_default
//   - DELETE /admin/ips/:id         — remove IP (409 if in use or default)
//   - GET    /user/ips              — list user-selectable subset (Step 5 will
//     use this for the user-shell domain picker; placed here because the
//     handler is a one-line filter on the same repo)
//   - GET    /internal/agent/managed-ips — agent fetches bound rows on start
//     (Step 4); behind RequireLocalhost so SPA never reaches it
func RegisterIPRoutes(g *gin.RouterGroup, cfg IPHandlerConfig) {
	if cfg.Repo == nil {
		panic("api.RegisterIPRoutes: cfg.Repo is nil")
	}
	h := &ipHandler{cfg: cfg}

	admin := g.Group("/admin/ips", middleware.RequireAdmin())
	admin.GET("", h.list)
	admin.POST("", h.create)
	admin.PATCH("/:id", h.update)
	admin.DELETE("/:id", h.delete)

	g.GET("/user/ips", h.userList)

	internal := g.Group("/internal/agent")
	internal.Use(middleware.RequireLocalhost())
	internal.GET("/managed-ips", h.internalList)
	internal.PATCH("/managed-ips/:id", h.internalPatch)
}

type ipHandler struct{ cfg IPHandlerConfig }

// ipListRow is ManagedIP plus a transient KernelPresent flag sourced from
// agent ip.list. Lets the UI distinguish "managed by jabali + on kernel"
// (bound), "external, netplan/cloud-init owns it" (system), and "lost
// from kernel" (degraded) without mutating the DB row's semantics — see
// reconciler/managed_ips.go for the is_bound=FALSE ownership rule.
//
// KernelPresent is a pointer so a failed agent ip.list call omits the
// field entirely (UI falls back to the is_bound-only view).
type ipListRow struct {
	models.ManagedIP
	KernelPresent *bool `json:"kernel_present,omitempty"`
}

// listResponse mirrors the {data,total,page,page_size} envelope used by
// /admin/packages, /admin/domains, etc. The IP pool is small (capped at
// 100 by validation) so we don't paginate, but the envelope shape is
// what the panel-ui hooks expect.
type ipListResponse struct {
	Data     []ipListRow `json:"data"`
	Total    int         `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

func (h *ipHandler) list(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.cfg.Repo.ListAll(ctx)
	if err != nil {
		h.cfg.Log.Error("ip list", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Optional ?family=ipv4|ipv6 filter — the UI reuses this for the
	// per-family picker dropdowns in Step 9.
	if fam := c.Query("family"); fam != "" {
		filtered := rows[:0]
		for _, r := range rows {
			if r.Family == fam {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	present := h.kernelPresentMap(ctx)
	out := make([]ipListRow, len(rows))
	for i := range rows {
		out[i].ManagedIP = rows[i]
		if present != nil {
			v := present[rows[i].Address]
			out[i].KernelPresent = &v
		}
	}
	c.JSON(http.StatusOK, ipListResponse{
		Data:     out,
		Total:    len(out),
		Page:     1,
		PageSize: len(out),
	})
}

// kernelPresentMap calls agent ip.list and returns an address→present
// map. Returns nil on any failure (agent down, parse error, flag off) so
// the caller omits the kernel_present field — UI degrades to the
// is_bound-only view.
func (h *ipHandler) kernelPresentMap(ctx context.Context) map[string]bool {
	if !h.cfg.AgentIPCommandsEnabled || h.cfg.Agent == nil {
		return nil
	}
	raw, err := h.cfg.Agent.Call(ctx, "ip.list", nil)
	if err != nil {
		return nil
	}
	var resp struct {
		Entries []struct {
			Address string `json:"address"`
		} `json:"entries"`
	}
	if jerr := json.Unmarshal(raw, &resp); jerr != nil {
		return nil
	}
	m := make(map[string]bool, len(resp.Entries))
	for _, e := range resp.Entries {
		m[e.Address] = true
	}
	return m
}

// userList returns the subset of the IP pool the operator has flagged
// as user-selectable. Same envelope as list; no admin guard.
func (h *ipHandler) userList(c *gin.Context) {
	rows, err := h.cfg.Repo.ListAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	out := make([]ipListRow, 0, len(rows))
	for _, r := range rows {
		if !r.IsUserSelectable {
			continue
		}
		if fam := c.Query("family"); fam != "" && r.Family != fam {
			continue
		}
		out = append(out, ipListRow{ManagedIP: r})
	}
	c.JSON(http.StatusOK, ipListResponse{
		Data:     out,
		Total:    len(out),
		Page:     1,
		PageSize: len(out),
	})
}

type createIPRequest struct {
	Address          string `json:"address"           binding:"required"`
	Label            string `json:"label"`
	IsUserSelectable bool   `json:"is_user_selectable"`
}

type ipResponse struct {
	models.ManagedIP
	// Warnings carries non-fatal post-bind issues (firewall probe etc.)
	// surfaced from the agent when AgentIPCommandsEnabled is on (Step 4).
	Warnings []string `json:"warnings,omitempty"`
}

func (h *ipHandler) create(c *gin.Context) {
	var req createIPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	addr := strings.TrimSpace(req.Address)
	family := models.DeriveFamily(addr)
	if family == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_address", "detail": "address is not a valid IPv4 or IPv6"})
		return
	}
	if err := validateRoutableIP(addr); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_address", "detail": err.Error()})
		return
	}

	row := &models.ManagedIP{
		Address:          addr,
		Family:           family,
		Label:            strings.TrimSpace(req.Label),
		IsUserSelectable: req.IsUserSelectable,
	}
	ctx := c.Request.Context()
	if err := h.cfg.Repo.Create(ctx, row); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "address_already_in_pool", "detail": "address " + addr + " already exists in managed_ips"})
			return
		}
		h.cfg.Log.Error("ip create", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	resp := ipResponse{ManagedIP: *row}

	// Step 4 wires the agent ip.bind call here. Until then this branch
	// is a no-op; the row exists but is_bound stays false until the
	// operator pre-binds externally or Step 4 ships.
	if h.cfg.AgentIPCommandsEnabled && h.cfg.Agent != nil {
		warnings, bindErr := h.bindOnAgent(ctx, row)
		if bindErr != nil {
			// Roll back the DB row — caller sees a clean 502 instead of
			// a half-created entry.
			_ = h.cfg.Repo.Delete(ctx, row.ID)
			respondAgentErr(c, "agent_bind_failed", bindErr)
			return
		}
		resp.Warnings = warnings
	}

	c.JSON(http.StatusCreated, resp)
}

type updateIPRequest struct {
	Label            *string `json:"label,omitempty"`
	IsUserSelectable *bool   `json:"is_user_selectable,omitempty"`
	IsDefault        *bool   `json:"is_default,omitempty"`
}

func (h *ipHandler) update(c *gin.Context) {
	id, err := parseIPID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	var req updateIPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	ctx := c.Request.Context()
	row, err := h.cfg.Repo.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if req.Label != nil {
		row.Label = strings.TrimSpace(*req.Label)
	}
	if req.IsUserSelectable != nil {
		row.IsUserSelectable = *req.IsUserSelectable
	}
	if req.IsDefault != nil && *req.IsDefault != row.IsDefault {
		// Promoting a new default demotes the previous default in the
		// same family. Undoing default (true→false) is rejected — the
		// operator must promote another row instead, so we never have
		// a family with no default.
		if !*req.IsDefault {
			c.JSON(http.StatusBadRequest, gin.H{"error": "default_required", "detail": "promote another IP to default first; cannot leave the family without one"})
			return
		}
		if err := h.promoteDefault(ctx, row); err != nil {
			h.cfg.Log.Error("ip promote default", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		row.IsDefault = true
	}

	if err := h.cfg.Repo.Update(ctx, row); err != nil {
		h.cfg.Log.Error("ip update", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, row)
}

// promoteDefault demotes the existing per-family default (if any) so
// only one row carries is_default=TRUE per family. Caller flips
// row.IsDefault on the in-memory copy after this returns.
func (h *ipHandler) promoteDefault(ctx context.Context, row *models.ManagedIP) error {
	current, err := h.cfg.Repo.FindDefaultByFamily(ctx, row.Family)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	if current == nil || current.ID == row.ID {
		return nil
	}
	current.IsDefault = false
	return h.cfg.Repo.Update(ctx, current)
}

func (h *ipHandler) delete(c *gin.Context) {
	id, err := parseIPID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	ctx := c.Request.Context()
	row, err := h.cfg.Repo.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if row.IsDefault {
		c.JSON(http.StatusConflict, gin.H{"error": "cannot_delete_default", "detail": "promote another " + row.Family + " IP to default first"})
		return
	}

	count, err := h.cfg.Repo.CountDomainsUsingIP(ctx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if count > 0 {
		// Surface the actual affected domains so the admin knows what to
		// reassign before retrying. The list is bounded to avoid huge
		// payloads on a misconfigured pool.
		domains, _ := h.affectedDomains(ctx, id)
		c.JSON(http.StatusConflict, gin.H{
			"error":            "ip_in_use",
			"detail":           "this IP is bound to one or more domains; reassign them before deleting",
			"affected_domains": domains,
			"affected_count":   count,
		})
		return
	}

	// Step 4 wires the agent ip.unbind call here.
	if h.cfg.AgentIPCommandsEnabled && h.cfg.Agent != nil && row.IsBound {
		if err := h.unbindOnAgent(ctx, row); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"error":  "agent_unbind_failed",
				"detail": "kernel binding removal failed: " + err.Error() + "; retry or unbind manually with `ip addr del`",
			})
			return
		}
	}

	if err := h.cfg.Repo.Delete(ctx, id); err != nil {
		h.cfg.Log.Error("ip delete", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Status(http.StatusNoContent)
}

// affectedDomains returns up to 50 domain names whose listen_ipv*_id
// references the given managed_ip — the cap protects the payload from
// blowing up on a misconfigured pool. The handler caller already has the
// authoritative count; this list is purely for the operator's UI.
func (h *ipHandler) affectedDomains(ctx context.Context, id uint64) ([]string, error) {
	if h.cfg.Domains == nil {
		return nil, nil
	}
	// Pool is small (≤100 IPs); pulling every domain once per delete is
	// cheap. List(ctx, ListOptions{}) returns the unfiltered set the
	// admin shell already uses elsewhere.
	all, _, err := h.cfg.Domains.List(ctx, repository.ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, d := range all {
		if (d.ListenIPv4ID != nil && *d.ListenIPv4ID == id) ||
			(d.ListenIPv6ID != nil && *d.ListenIPv6ID == id) {
			out = append(out, d.Name)
			if len(out) >= 50 {
				break
			}
		}
	}
	return out, nil
}

// internalList is the agent-side endpoint exposed only to localhost.
// Returns every managed_ips row (bound and unbound) so the agent's
// reconcile-on-start loop knows which addresses it should ensure live
// on the kernel.
func (h *ipHandler) internalList(c *gin.Context) {
	rows, err := h.cfg.Repo.ListAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ips": rows})
}

type internalPatchRequest struct {
	IsBound  *bool `json:"is_bound,omitempty"`
	Degraded *bool `json:"degraded,omitempty"`
}

func (h *ipHandler) internalPatch(c *gin.Context) {
	id, err := parseIPID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	var req internalPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	ctx := c.Request.Context()
	row, err := h.cfg.Repo.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if req.IsBound != nil {
		row.IsBound = *req.IsBound
	}
	if req.Degraded != nil {
		row.Degraded = *req.Degraded
	}
	if err := h.cfg.Repo.Update(ctx, row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, row)
}

// bindOnAgent calls ip.bind and translates the response. Returns the
// list of warnings (e.g. firewall probe failures) and an error only on
// hard bind failure. Step 4 contains this function's body — Step 2
// stubs it because the feature flag is off.
func (h *ipHandler) bindOnAgent(ctx context.Context, row *models.ManagedIP) ([]string, error) {
	raw, err := h.cfg.Agent.Call(ctx, "ip.bind", map[string]any{
		"address": row.Address,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Bound          bool     `json:"bound"`
		Reachable      bool     `json:"reachable"`
		SuspectedCause string   `json:"suspected_cause,omitempty"`
		Warnings       []string `json:"warnings,omitempty"`
	}
	if jerr := json.Unmarshal(raw, &resp); jerr != nil {
		return nil, jerr
	}
	row.IsBound = resp.Bound
	if resp.Bound && !resp.Reachable {
		row.Degraded = true
		_ = h.cfg.Repo.Update(ctx, row)
		return append(resp.Warnings, "connectivity probe failed; verify host firewall allows inbound to this address ("+resp.SuspectedCause+")"), nil
	}
	if resp.Bound {
		_ = h.cfg.Repo.Update(ctx, row)
	}
	return resp.Warnings, nil
}

func (h *ipHandler) unbindOnAgent(ctx context.Context, row *models.ManagedIP) error {
	_, err := h.cfg.Agent.Call(ctx, "ip.unbind", map[string]any{
		"address": row.Address,
	})
	return err
}

// validateRoutableIP rejects loopback, link-local, multicast, and
// unspecified addresses — none of those are useful as a public-facing
// vhost listen target and binding them would either be a no-op or
// dangerous.
func validateRoutableIP(addr string) error {
	ip := net.ParseIP(addr)
	if ip == nil {
		return errors.New("not a valid IP")
	}
	if ip.IsLoopback() {
		return errors.New("loopback addresses cannot be in the IP pool")
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return errors.New("link-local addresses cannot be in the IP pool")
	}
	if ip.IsMulticast() {
		return errors.New("multicast addresses cannot be in the IP pool")
	}
	if ip.IsUnspecified() {
		return errors.New("unspecified address (0.0.0.0 / ::) cannot be in the IP pool")
	}
	return nil
}

func parseIPID(s string) (uint64, error) {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}
