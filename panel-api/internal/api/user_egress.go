package api

import (
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/egressops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// maxAllowedExtras caps the per-user override list size. Hard ceiling
// rather than a per-row vs per-user limit — the nft renderer emits one
// rule per entry, and a runaway list would inflate the rule file
// without bound.
const maxAllowedExtras = 50

// UserEgressHandlerConfig wires the M34 admin + user-facing egress
// endpoints. All four repos are required; if any are nil the routes
// stay unmounted (matches the conditional pattern from M18).
type UserEgressHandlerConfig struct {
	Users       repository.UserRepository
	Policies    repository.UserEgressPolicyRepository
	Requests    repository.UserEgressRequestRepository
	// DropSamples backs GET /admin/users/:id/egress/drops-24h —
	// the M34 deep-stats sparkline. Optional; nil → endpoint
	// returns 503.
	DropSamples repository.UserEgressDropSampleRepository
	// Agent (GH #713) backs the drop-events drill-down (reads the nft drop log).
	Agent agent.AgentInterface
	// PinPath overrides the operator-pin file for the mature-soak promotion
	// endpoints (JAB-13). Empty → egressops.DefaultPinPath. Injectable for tests.
	PinPath string
}

// RegisterAdminUserEgressRoutes mounts the admin-side egress endpoints.
// Caller is responsible for ensuring g already has RequireAdmin().
func RegisterAdminUserEgressRoutes(g *gin.RouterGroup, cfg UserEgressHandlerConfig) {
	h := &userEgressHandler{cfg: cfg}
	g.GET("/users/:id/egress", h.adminGet)
	g.PUT("/users/:id/egress", h.adminPut)
	g.GET("/egress-requests", h.listRequests)
	g.POST("/egress-requests/:id/approve", h.approveRequest)
	g.POST("/egress-requests/:id/deny", h.denyRequest)
	g.GET("/egress-summary", h.summary)
	g.GET("/users/:id/egress/drops-24h", h.adminDrops24h)
	g.GET("/users/:id/egress/drop-events", h.adminDropEvents) // GH #713
	// JAB-13: mature-soak promotion — preview (dry-run) + confirm-gated flip.
	g.GET("/egress/mature-preview", h.maturePreview)
	g.POST("/egress/flip-mature", h.flipMature)
}

// RegisterMeEgressRoutes mounts the user-facing /me/egress endpoints.
// Auth comes from the parent group's RequireAuth middleware; the
// handlers resolve the user from the JWT, so no path parameter is
// required.
func RegisterMeEgressRoutes(g *gin.RouterGroup, cfg UserEgressHandlerConfig) {
	h := &userEgressHandler{cfg: cfg}
	g.GET("/me/egress", h.meGet)
	g.GET("/me/egress/requests", h.meListRequests)
	g.POST("/me/egress/request", h.meCreateRequest)
	g.DELETE("/me/egress/requests/:id", h.meCancelRequest)
}

type userEgressHandler struct{ cfg UserEgressHandlerConfig }

// egressDestinationInput is the request-body shape for a single
// allowed-destination row. Mirrors models.EgressDestination but with
// validation hooks during binding.
type egressDestinationInput struct {
	CIDR     string `json:"cidr"     binding:"required"`
	Port     *int   `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

type adminEgressPutBody struct {
	State        string                   `json:"state"        binding:"required"`
	AllowedExtra []egressDestinationInput `json:"allowed_extra"`
}

// adminGet returns the policy for one user. EnsureDefault is called so
// callers always get a row back (state=enforced + empty allowlist if
// the user pre-dates the M34 migration), avoiding 404 noise in the UI.
func (h *userEgressHandler) adminGet(c *gin.Context) {
	userID := c.Param("id")
	if _, err := h.cfg.Users.FindByID(c.Request.Context(), userID); err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if err := h.cfg.Policies.EnsureDefault(c.Request.Context(), userID, ""); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ensure_default", "detail": err.Error()})
		return
	}
	row, err := h.cfg.Policies.Get(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "fetch", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.policyView(row))
}

// adminPut replaces the policy for one user. Validates state + every
// destination at the API boundary so invalid rows never land in the
// reconciler queue.
func (h *userEgressHandler) adminPut(c *gin.Context) {
	userID := c.Param("id")
	if _, err := h.cfg.Users.FindByID(c.Request.Context(), userID); err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	var body adminEgressPutBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "detail": err.Error()})
		return
	}
	if !validEgressState(body.State) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_state"})
		return
	}
	if len(body.AllowedExtra) > maxAllowedExtras {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too_many_extras", "max": maxAllowedExtras})
		return
	}
	for i := range body.AllowedExtra {
		if err := validateExtra(&body.AllowedExtra[i]); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_destination", "detail": err.Error(), "index": i})
			return
		}
	}

	extras := convertExtras(body.AllowedExtra)
	jsonExtras, _ := json.Marshal(extras)
	claims := ginctx.Claims(c)
	var updatedBy *string
	if claims != nil && claims.UserID != "" {
		uid := claims.UserID
		updatedBy = &uid
	}

	policy := &models.UserEgressPolicy{
		UserID:       userID,
		State:        body.State,
		AllowedExtra: jsonExtras,
		UpdatedBy:    updatedBy,
	}
	if err := h.cfg.Policies.Upsert(c.Request.Context(), policy); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "upsert", "detail": err.Error()})
		return
	}
	row, err := h.cfg.Policies.Get(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "fetch_after_upsert", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.policyView(row))
}

// listRequests is the admin queue view — every pending row.
func (h *userEgressHandler) listRequests(c *gin.Context) {
	rows, err := h.cfg.Requests.ListPending(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list", "detail": err.Error()})
		return
	}
	if rows == nil {
		rows = []models.UserEgressRequest{}
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows)})
}

// approveRequest decides a request, and if approved folds the
// destination into the user's allowed_extra list. Idempotent on
// already-approved rows (returns 409).
func (h *userEgressHandler) approveRequest(c *gin.Context) {
	h.decideRequest(c, models.UserEgressRequestStatusApproved)
}

// denyRequest decides a request as denied. The user can submit a new
// request afterwards if they want to retry.
func (h *userEgressHandler) denyRequest(c *gin.Context) {
	h.decideRequest(c, models.UserEgressRequestStatusDenied)
}

func (h *userEgressHandler) decideRequest(c *gin.Context, status string) {
	requestID := c.Param("id")
	claims := ginctx.Claims(c)
	reviewedBy := ""
	if claims != nil {
		reviewedBy = claims.UserID
	}

	req, err := egressops.DecideRequest(c.Request.Context(),
		egressops.Deps{Requests: h.cfg.Requests, Policies: h.cfg.Policies},
		requestID, status, reviewedBy)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "request_not_found"})
		case errors.Is(err, egressops.ErrAlreadyDecided):
			c.JSON(http.StatusConflict, gin.H{"error": "already_decided", "status": req.Status})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decide", "detail": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": requestID, "status": status})
}

// summary returns a quick aggregate for the admin dashboard widget.
// State counts + total drops in the last 24h, in one round-trip.
func (h *userEgressHandler) summary(c *gin.Context) {
	counts, err := h.cfg.Policies.StateCounts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "state_counts"})
		return
	}
	policies, err := h.cfg.Policies.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_policies"})
		return
	}
	var totalDrops uint64
	for _, p := range policies {
		totalDrops += p.DropCount24h
	}
	c.JSON(http.StatusOK, gin.H{
		"state_counts":  counts,
		"total_drops":   totalDrops,
		"policy_total":  len(policies),
	})
}

// meGet returns the caller's own policy. Always returns a row by
// EnsureDefault — never 404.
func (h *userEgressHandler) meGet(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	if err := h.cfg.Policies.EnsureDefault(c.Request.Context(), claims.UserID, ""); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ensure_default", "detail": err.Error()})
		return
	}
	row, err := h.cfg.Policies.Get(c.Request.Context(), claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "fetch"})
		return
	}
	c.JSON(http.StatusOK, h.policyView(row))
}

// meListRequests returns the caller's own request history.
func (h *userEgressHandler) meListRequests(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	rows, err := h.cfg.Requests.ListByUser(c.Request.Context(), claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list", "detail": err.Error()})
		return
	}
	if rows == nil {
		rows = []models.UserEgressRequest{}
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows)})
}

type meEgressRequestBody struct {
	CIDR     string `json:"cidr"     binding:"required"`
	Port     *uint  `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Reason   string `json:"reason"   binding:"required"`
}

// meCreateRequest writes a pending row owned by the caller. Reason is
// required so the admin queue carries enough context to decide.
func (h *userEgressHandler) meCreateRequest(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	var body meEgressRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "detail": err.Error()})
		return
	}
	if _, _, err := net.ParseCIDR(body.CIDR); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cidr", "detail": err.Error()})
		return
	}
	proto := body.Protocol
	if proto == "" {
		proto = models.UserEgressProtocolTCP
	}
	if proto != models.UserEgressProtocolTCP && proto != models.UserEgressProtocolUDP {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_protocol"})
		return
	}
	if body.Port != nil && (*body.Port == 0 || *body.Port > 65535) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_port"})
		return
	}
	if len(strings.TrimSpace(body.Reason)) < 4 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason_too_short", "min": 4})
		return
	}
	if len(body.Reason) > 500 {
		body.Reason = body.Reason[:500]
	}

	req := &models.UserEgressRequest{
		ID:        ids.NewULID(),
		UserID:    claims.UserID,
		CIDR:      body.CIDR,
		Port:      body.Port,
		Protocol:  proto,
		Reason:    body.Reason,
		Status:    models.UserEgressRequestStatusPending,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.cfg.Requests.Create(c.Request.Context(), req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, req)
}

// meCancelRequest hard-deletes a pending row owned by the caller. Repo
// matches on (id, user_id, status=pending) so the call cannot reach a
// row from a different user nor a row already decided.
func (h *userEgressHandler) meCancelRequest(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	if err := h.cfg.Requests.CancelPending(c.Request.Context(), c.Param("id"), claims.UserID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "request_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cancel"})
		return
	}
	c.Status(http.StatusNoContent)
}

// policyView reshapes the DB row for the wire — surfaces the decoded
// allowed_extra list (never null) and inlines a typed shape.
func (h *userEgressHandler) policyView(row *models.UserEgressPolicy) gin.H {
	extras, _ := row.DecodeAllowedExtra()
	return gin.H{
		"user_id":             row.UserID,
		"state":               row.State,
		"allowed_extra":       extras,
		"drop_count_24h":      row.DropCount24h,
		"drop_count_at":       row.DropCountAt,
		"learning_started_at": row.LearningStartedAt,
		"updated_at":          row.UpdatedAt,
		"updated_by":          row.UpdatedBy,
	}
}

func validEgressState(s string) bool {
	return s == models.UserEgressStateOff ||
		s == models.UserEgressStateLearning ||
		s == models.UserEgressStateEnforced
}

func validateExtra(e *egressDestinationInput) error {
	if _, _, err := net.ParseCIDR(e.CIDR); err != nil {
		return errors.New("cidr: " + err.Error())
	}
	if e.Port != nil && (*e.Port < 1 || *e.Port > 65535) {
		return errors.New("port out of range")
	}
	if e.Protocol == "" {
		e.Protocol = models.UserEgressProtocolTCP
	}
	if e.Protocol != models.UserEgressProtocolTCP && e.Protocol != models.UserEgressProtocolUDP {
		return errors.New("protocol must be tcp or udp")
	}
	if len(e.Comment) > 200 {
		return errors.New("comment too long")
	}
	return nil
}

func convertExtras(in []egressDestinationInput) []models.EgressDestination {
	out := make([]models.EgressDestination, 0, len(in))
	for _, e := range in {
		out = append(out, models.EgressDestination{
			CIDR: e.CIDR, Port: e.Port,
			Protocol: e.Protocol, Comment: e.Comment,
		})
	}
	return out
}

// adminDrops24h returns 24 hourly buckets of dropped-packet counts
// for the given user. Drives the sparkline on the admin Egress card.
//
// Bucketing happens here (not in SQL) because the per-tick samples
// are at minute-ish granularity and the sparkline only wants 24
// data points. Cheap: the query already caps at 24h × 60s ticks ≈
// 1440 rows max per user.
func (h *userEgressHandler) adminDrops24h(c *gin.Context) {
	if h.cfg.DropSamples == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "drop-samples repo not wired"})
		return
	}
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id required"})
		return
	}
	now := time.Now().UTC()
	rows, err := h.cfg.DropSamples.ListLast24h(c.Request.Context(), userID, now)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list samples: " + err.Error()})
		return
	}

	// Bucket into 24 hourly slots. Slot index = hours since
	// (now - 24h), so [0]=oldest, [23]=current hour.
	buckets := make([]uint64, 24)
	bucketStart := now.Add(-24 * time.Hour)
	for _, r := range rows {
		offset := r.At.Sub(bucketStart)
		idx := int(offset.Hours())
		if idx < 0 {
			continue
		}
		if idx > 23 {
			idx = 23
		}
		buckets[idx] += r.Drops
	}

	out := make([]map[string]any, 24)
	for i := 0; i < 24; i++ {
		out[i] = map[string]any{
			"at":    bucketStart.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
			"drops": buckets[i],
		}
	}
	c.JSON(http.StatusOK, gin.H{"user_id": userID, "buckets": out})
}

// adminDropEvents (GH #713) returns recent per-flow egress DROP events for a
// user — destination, port, proto, aggregated count, last-seen — parsed from the
// nft drop log (the sparkline's counter samples can't show WHAT was blocked).
func (h *userEgressHandler) adminDropEvents(c *gin.Context) {
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent not wired"})
		return
	}
	userID := c.Param("id")
	u, err := h.cfg.Users.FindByID(c.Request.Context(), userID)
	if err != nil || u == nil || u.Username == nil || *u.Username == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found"})
		return
	}
	raw, aerr := h.cfg.Agent.Call(c.Request.Context(), "security.egress.drops", map[string]any{"username": *u.Username})
	if aerr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "drop_events_failed", "detail": aerr.Error()})
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}


// pinPath resolves the operator-pin file, defaulting to the shared constant.
func (h *userEgressHandler) pinPath() string {
	if h.cfg.PinPath != "" {
		return h.cfg.PinPath
	}
	return egressops.DefaultPinPath
}

// flipMatureEligible is one LEARNING policy that is (or would be) promoted,
// with the soak-decision signals the GUI shows: how long it has been learning
// and how many drops it saw in the last 24h.
type flipMatureEligible struct {
	UserID            string     `json:"user_id"`
	LearningStartedAt *time.Time `json:"learning_started_at,omitempty"`
	AgeDays           int        `json:"age_days"`
	DropCount24h      uint64     `json:"drop_count_24h"`
}

// flipMatureResponse is the shared shape for the preview + flip endpoints so the
// GUI renders both from one type. flipped/failed are empty on a dry-run preview.
type flipMatureResponse struct {
	SoakDays      int                  `json:"soak_days"`
	DryRun        bool                 `json:"dry_run"`
	Pinned        bool                 `json:"pinned"`
	PinValue      string               `json:"pin_value,omitempty"`
	EligibleCount int                  `json:"eligible_count"`
	Eligible      []flipMatureEligible `json:"eligible"`
	Flipped       []string             `json:"flipped,omitempty"`
	FlippedCount  int                  `json:"flipped_count"`
	Failed        map[string]string    `json:"failed,omitempty"`
}

func newFlipMatureResponse(res *egressops.FlipMatureResult) flipMatureResponse {
	out := flipMatureResponse{
		SoakDays:      res.SoakDays,
		DryRun:        res.DryRun,
		Pinned:        res.Pinned,
		PinValue:      res.PinValue,
		EligibleCount: len(res.Eligible),
		Eligible:      make([]flipMatureEligible, 0, len(res.Eligible)),
		Flipped:       res.Flipped,
		FlippedCount:  len(res.Flipped),
	}
	now := time.Now().UTC()
	for _, r := range res.Eligible {
		e := flipMatureEligible{
			UserID:            r.UserID,
			LearningStartedAt: r.LearningStartedAt,
			DropCount24h:      r.DropCount24h,
		}
		if r.LearningStartedAt != nil {
			e.AgeDays = int(now.Sub(r.LearningStartedAt.UTC()).Hours() / 24)
		}
		out.Eligible = append(out.Eligible, e)
	}
	if len(res.Failed) > 0 {
		out.Failed = res.Failed
	}
	return out
}

// soakDaysFromQuery parses ?soak_days=N, defaulting to 7 and clamping the lower
// bound so egressops.FlipMature's >0 guard is never the failure path for a GET.
func soakDaysFromQuery(c *gin.Context) int {
	v := c.DefaultQuery("soak_days", "7")
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 7
	}
	return n
}

// maturePreview (GET /egress/mature-preview) is the dry-run: which LEARNING
// policies are mature enough to flip, and whether the operator pin blocks it.
func (h *userEgressHandler) maturePreview(c *gin.Context) {
	res, err := egressops.FlipMature(c.Request.Context(), h.cfg.Policies, soakDaysFromQuery(c), h.pinPath(), true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mature_preview", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, newFlipMatureResponse(res))
}

// flipMature (POST /egress/flip-mature) promotes mature LEARNING policies to
// ENFORCED. Confirm-gated in the GUI; the admin-route audit middleware records
// the actor + request. No-op when the operator pin holds.
func (h *userEgressHandler) flipMature(c *gin.Context) {
	var body struct {
		SoakDays int `json:"soak_days"`
	}
	// Body is optional; default the soak window when omitted.
	_ = c.ShouldBindJSON(&body)
	soakDays := body.SoakDays
	if soakDays <= 0 {
		soakDays = 7
	}
	res, err := egressops.FlipMature(c.Request.Context(), h.cfg.Policies, soakDays, h.pinPath(), false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "flip_mature", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, newFlipMatureResponse(res))
}
