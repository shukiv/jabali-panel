package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DatabaseUserHandlerConfig plugs the database user handlers into the router.
type DatabaseUserHandlerConfig struct {
	Databases      repository.DatabaseRepository
	DatabaseUsers  repository.DatabaseUserRepository
	DatabaseGrants repository.DatabaseUserGrantRepository
	Users          repository.UserRepository
	Packages       repository.PackageRepository
	ServerSettings repository.ServerSettingsRepository
	Agent          agent.AgentInterface
}

const (
	defaultDatabaseUsersPageSize = 20
	maxDatabaseUsersPageSize     = 200
)

// RegisterDatabaseUserRoutes mounts /database-users* and /database-user-grants* under g.
//
// The user and grant lifecycles are deliberately separate: a database
// user is a MariaDB account (username + password), independent of any
// database access. Granting access to a specific database is a second
// step (POST /database-users/:id/grants), and a user can hold any
// number of grants across different databases. Revoking is per-grant
// — deleting the whole user cascades all its grants.
//
// - GET /database-users                 list users (grants embedded)
// - POST /database-users                create user (username only; returns plaintext password once)
// - DELETE /database-users/:id          drop user + cascade revoke all grants
// - POST /database-users/:id/rotate-password    rotate password
// - POST /database-users/:id/grants     add a grant (database_id + grant_level)
// - PATCH /database-user-grants/:id     change a grant's level in place
// - DELETE /database-user-grants/:id    revoke a single grant, keep the user
func RegisterDatabaseUserRoutes(g *gin.RouterGroup, cfg DatabaseUserHandlerConfig) {
	h := &databaseUserHandler{cfg: cfg}

	users := g.Group("/database-users")
	users.GET("", h.list)
	users.POST("", h.create)
	users.DELETE("/:id", h.delete)
	users.POST("/:id/rotate-password", h.rotatePassword)
	users.POST("/:id/grants", h.addGrant)

	grants := g.Group("/database-user-grants")
	grants.PATCH("/:id", h.updateGrant)
	grants.DELETE("/:id", h.deleteGrant)
}

type databaseUserHandler struct{ cfg DatabaseUserHandlerConfig }

// parsePrivilegesFromString splits a comma-separated privilege string into a slice.
// Returns nil for empty string.
func parsePrivilegesFromString(s string) []string {
	if s == "" {
		return nil
	}
	if s == "ALL" {
		return []string{"ALL"}
	}
	var result []string
	// Split by comma and trim whitespace.
	parts := strings.Split(s, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// privilegesToCanonicalString converts a privileges slice to the canonical form:
// ["SELECT","INSERT","UPDATE","DELETE","CREATE","DROP","ALTER","INDEX"] → "SELECT,INSERT,UPDATE,DELETE,CREATE,DROP,ALTER,INDEX"
// ["ALL"] → "ALL"
// Returns empty string if the slice is empty or contains only whitespace.
func privilegesToCanonicalString(privs []string) (string, error) {
	if len(privs) == 0 {
		return "", nil
	}

	// Check if "ALL" is present.
	for _, p := range privs {
		if p == "ALL" {
			return "ALL", nil
		}
	}

	// Canonical order and valid tokens.
	canonicalOrder := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "INDEX"}
	validTokens := map[string]bool{
		"SELECT": true,
		"INSERT": true,
		"UPDATE": true,
		"DELETE": true,
		"CREATE": true,
		"DROP":   true,
		"ALTER":  true,
		"INDEX":  true,
	}

	// Validate and collect seen privileges.
	seen := make(map[string]bool)
	for _, p := range privs {
		if !validTokens[p] {
			return "", fmt.Errorf("invalid privilege: %s", p)
		}
		seen[p] = true
	}

	// Build canonical string in order.
	var result []string
	for _, canonical := range canonicalOrder {
		if seen[canonical] {
			result = append(result, canonical)
		}
	}

	if len(result) == 0 {
		return "", nil
	}

	return strings.Join(result, ","), nil
}

// CanonicalDBGrant resolves a grant's privilege inputs into the stored
// canonical privilege string + the computed grant_level, applying the same
// validation the REST handler and the operator CLI both use (JAB-284) so the
// two adapters can never drift. Exactly one of an explicit privileges list or a
// rw/ro level must be supplied: rw = ALL, ro = SELECT; a custom list that is
// not exactly ALL/SELECT stores with level "custom".
func CanonicalDBGrant(privileges []string, level string) (canonical, computedLevel string, err error) {
	switch {
	case len(privileges) > 0:
		canonical, err = privilegesToCanonicalString(privileges)
		if err != nil {
			return "", "", err
		}
	case level == "rw":
		canonical = "ALL"
	case level == "ro":
		canonical = "SELECT"
	case level != "":
		return "", "", fmt.Errorf("grant_level must be 'rw' or 'ro' (got %q)", level)
	default:
		return "", "", fmt.Errorf("either privileges or grant_level must be provided")
	}
	computedLevel = "custom"
	switch canonical {
	case "ALL":
		computedLevel = "rw"
	case "SELECT":
		computedLevel = "ro"
	}
	return canonical, computedLevel, nil
}

// ---- Request/Response types ----

type createDatabaseUserRequest struct {
	Username string `json:"username" binding:"required"`
	// M37 Phase 3 — engine picks the DB-level backend the role lives
	// in. Empty defaults to 'mariadb' (back-compat). 'postgres'
	// requires server_settings.postgres_enabled.
	Engine string `json:"engine,omitempty"`
}

type createDatabaseUserResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	// Password is returned exactly once (plaintext). We store only the
	// bcrypt hash — the caller MUST save this now or rotate later.
	Password string `json:"password"`
}

// addGrantRequest is the body for POST /database-users/:id/grants.
type addGrantRequest struct {
	DatabaseID string   `json:"database_id" binding:"required"`
	GrantLevel string   `json:"grant_level"`
	Privileges []string `json:"privileges"`
}

// grantResponse is the shape of a single grant — used inline in the
// user-list response and as the return body of the add/update endpoints.
type grantResponse struct {
	ID           string   `json:"id"`
	DatabaseID   string   `json:"database_id"`
	DatabaseName string   `json:"database_name"`
	GrantLevel   string   `json:"grant_level"`
	Privileges   []string `json:"privileges"`
}

type rotateDatabaseUserPasswordRequest struct {
	NewPassword string `json:"new_password" binding:"required"`
}

type rotateDatabaseUserPasswordResponse struct {
	Password string `json:"password"`
}

type updateDatabaseUserGrantRequest struct {
	GrantLevel string   `json:"grant_level"`
	Privileges []string `json:"privileges"`
}

// ---- M37 engine dispatch helpers ----
//
// dbUserCmd returns the agent command name for a (engine, action)
// pair. action ∈ {drop, grant, revoke, rotate_password}. Centralised
// so call sites stay small + dispatch table is in one place.
func dbUserCmd(engine, action string) string {
	if engine == "postgres" {
		switch action {
		case "drop":
			return "db.postgres.drop_role"
		case "grant":
			return "db.postgres.grant"
		case "revoke":
			return "db.postgres.revoke"
		case "rotate_password":
			// pg has no separate rotate; create_role's idempotent
			// upsert via DO block ALTERs the password if the role
			// already exists.
			return "db.postgres.create_role"
		}
	}
	switch action {
	case "drop":
		return "db_user.drop"
	case "grant":
		return "db_user.grant"
	case "revoke":
		return "db_user.revoke"
	case "rotate_password":
		return "db_user.rotate_password"
	}
	return ""
}

// dbUserDropParams returns the param shape for a drop call. MariaDB
// uses db_user_name; Postgres uses role.
func dbUserDropParams(engine, username string) map[string]any {
	if engine == "postgres" {
		return map[string]any{"role": username}
	}
	return map[string]any{"db_user_name": username}
}

// dbUserGrantParams covers grant + revoke. MariaDB also takes a
// grant_level (rw|ro) which Postgres maps to a single GRANT ALL
// (per-table grants are deferred per ADR-0091).
func dbUserGrantParams(engine, dbName, username, grantLevel string) map[string]any {
	if engine == "postgres" {
		return map[string]any{"db_name": dbName, "role": username}
	}
	return map[string]any{
		"db_name":      dbName,
		"db_user_name": username,
		"grant_level":  grantLevel,
	}
}

// dbUserRotateParams: MariaDB takes db_user_name+password; Postgres
// reuses create_role's upsert path (role+password).
func dbUserRotateParams(engine, username, password string) map[string]any {
	if engine == "postgres" {
		return map[string]any{"role": username, "password": password}
	}
	return map[string]any{"db_user_name": username, "password": password}
}

// ---- Handlers ----

func (h *databaseUserHandler) list(c *gin.Context) {
	page, pageSize, opts := parseListOptions(c, defaultDatabaseUsersPageSize, maxDatabaseUsersPageSize)

	// Get current user claims
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var users []models.DatabaseUser
	var total int64
	var err error

	// Admins see all database users; users see only their own
	if claims.IsAdmin {
		users, total, err = h.cfg.DatabaseUsers.List(c.Request.Context(), opts)
	} else {
		users, total, err = h.cfg.DatabaseUsers.ListByUserID(c.Request.Context(), claims.UserID, opts)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if users == nil {
		users = []models.DatabaseUser{}
	}

	// For non-admins, enforce the panel-wide naming convention: users
	// only see rows whose username has their linux-username prefix.
	// Mirrors the filter in GET /databases.
	if !claims.IsAdmin {
		if u, uErr := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID); uErr == nil && u != nil && u.Username != nil && *u.Username != "" {
			prefix := *u.Username + "_"
			filtered := users[:0]
			for _, du := range users {
				if strings.HasPrefix(du.Username, prefix) {
					filtered = append(filtered, du)
				}
			}
			if len(filtered) != len(users) {
				total -= int64(len(users) - len(filtered))
			}
			users = filtered
		}
	}

	// Batch-fetch all grants for the returned users, then resolve
	// database names in one more pass — two queries regardless of
	// page size.
	userIDs := make([]string, 0, len(users))
	for _, u := range users {
		userIDs = append(userIDs, u.ID)
	}
	grants, err := h.cfg.DatabaseGrants.ListByDatabaseUserIDs(c.Request.Context(), userIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	dbNameByID := map[string]string{}
	for _, g := range grants {
		if _, ok := dbNameByID[g.DatabaseID]; ok {
			continue
		}
		d, dErr := h.cfg.Databases.FindByID(c.Request.Context(), g.DatabaseID)
		if dErr == nil {
			dbNameByID[g.DatabaseID] = d.Name
		}
	}
	grantsByUser := map[string][]grantResponse{}
	for _, g := range grants {
		grantsByUser[g.DatabaseUserID] = append(grantsByUser[g.DatabaseUserID], grantResponse{
			ID:           g.ID,
			DatabaseID:   g.DatabaseID,
			DatabaseName: dbNameByID[g.DatabaseID],
			GrantLevel:   g.GrantLevel,
			Privileges:   parsePrivilegesFromString(g.Privileges),
		})
	}

	// Re-shape users into an envelope that includes the grant list.
	type userRow struct {
		ID        string          `json:"id"`
		UserID    string          `json:"user_id"`
		Username  string          `json:"username"`
		Engine    string          `json:"engine"`
		CreatedAt time.Time       `json:"created_at"`
		UpdatedAt time.Time       `json:"updated_at"`
		Grants    []grantResponse `json:"grants"`
	}
	out := make([]userRow, len(users))
	for i, u := range users {
		g := grantsByUser[u.ID]
		if g == nil {
			g = []grantResponse{}
		}
		eng := u.Engine
		if eng == "" {
			eng = "mariadb"
		}
		out[i] = userRow{
			ID:        u.ID,
			UserID:    u.UserID,
			Username:  u.Username,
			Engine:    eng,
			CreatedAt: u.CreatedAt,
			UpdatedAt: u.UpdatedAt,
			Grants:    g,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *databaseUserHandler) create(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req createDatabaseUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	if !databaseUserNameValid(req.Username) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_username",
			"detail": "username must match regex ^[a-z][a-z0-9_]{0,63}$",
		})
		return
	}

	ctx := c.Request.Context()
	targetUserID := claims.UserID

	// Load panel user to enforce prefix + quota.
	user, err := h.cfg.Users.FindByID(ctx, targetUserID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if !claims.IsAdmin && (user.Username == nil || *user.Username == "") {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Enforce max_database_users quota if the user has a package.
	if user.PackageID != nil && *user.PackageID != "" {
		pkg, pkgErr := h.cfg.Packages.FindByID(ctx, *user.PackageID)
		if pkgErr == nil && pkg.MaxDatabaseUsers > 0 {
			limit := int64(pkg.MaxDatabaseUsers)
			count, cErr := h.cfg.DatabaseUsers.CountByUserID(ctx, targetUserID)
			if cErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
			if count >= limit {
				c.JSON(http.StatusConflict, gin.H{
					"error":    "quota_exceeded",
					"resource": "database_users",
					"limit":    limit,
				})
				return
			}
		}
	}

	// Final MariaDB-side name carries the panel username as a prefix
	// (admin creates use the bare name). We store the prefixed value so
	// what the UI shows matches what MariaDB sees.
	var finalUsername string
	if claims.IsAdmin {
		finalUsername = req.Username
	} else {
		finalUsername = *user.Username + "_" + req.Username
	}

	exists, err := h.cfg.DatabaseUsers.ExistsByUserAndUsername(ctx, targetUserID, finalUsername)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "username_exists"})
		return
	}

	plainPassword := ids.NewSecret()
	hash, err := bcrypt.GenerateFromPassword([]byte(plainPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// M37 engine dispatch on user create.
	engine := req.Engine
	if engine == "" {
		engine = "mariadb"
	}
	if engine != "mariadb" && engine != "postgres" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_engine"})
		return
	}
	if engine == "postgres" && h.cfg.ServerSettings != nil {
		settings, sErr := h.cfg.ServerSettings.Get(ctx)
		if sErr != nil || settings == nil || !settings.PostgresEnabled {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "postgres_disabled"})
			return
		}
	}

	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if engine == "mariadb" {
		_, err = h.cfg.Agent.Call(agentCtx, "db_user.create", map[string]any{
			"db_user_name": finalUsername,
			"password":     plainPassword,
		})
	} else {
		_, err = h.cfg.Agent.Call(agentCtx, "db.postgres.create_role", map[string]any{
			"role":     finalUsername,
			"password": plainPassword,
		})
	}
	if err != nil {
		respondAgentErr(c, "agent_failed", err)
		return
	}

	now := time.Now().UTC()
	du := &models.DatabaseUser{
		ID:           ids.NewULID(),
		UserID:       targetUserID,
		Username:     finalUsername,
		Engine:       engine,
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.cfg.DatabaseUsers.Create(ctx, du); err != nil {
		// On DB failure we've already provisioned the MariaDB user —
		// roll back so state is consistent.
		dropCtx, dropCancel := context.WithTimeout(ctx, 30*time.Second)
		defer dropCancel()
		_, _ = h.cfg.Agent.Call(dropCtx, dbUserCmd(engine, "drop"), dbUserDropParams(engine, finalUsername))
		if isConflict(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "username_exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.JSON(http.StatusCreated, createDatabaseUserResponse{
		ID:       du.ID,
		Username: du.Username,
		Password: plainPassword,
	})
}

// addGrant handles POST /database-users/:id/grants. Grant = one row in
// database_user_grants + a MariaDB GRANT statement run by the agent.
// Both sides are best-effort serialized; if the agent call fails we
// abort without writing the row, and if the row insert fails we
// REVOKE.
func (h *databaseUserHandler) addGrant(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req addGrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	// Canonicalise the privilege set + compute the stored grant_level. Shared
	// with the operator CLI (JAB-284) so both adapters validate + normalise
	// privileges identically and can never drift.
	canonicalPrivileges, computedGrantLevel, err := CanonicalDBGrant(req.Privileges, req.GrantLevel)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant", "detail": err.Error()})
		return
	}

	du, err := h.cfg.DatabaseUsers.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && du.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	db, err := h.cfg.Databases.FindByID(ctx, req.DatabaseID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "database_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && db.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Reject duplicates — the DB and the MariaDB side both treat a
	// repeat GRANT as a no-op, but we still want a clean error code so
	// the UI can message.
	if existing, _ := h.cfg.DatabaseGrants.FindByDBAndDBUser(ctx, db.ID, du.ID); existing != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "grant_exists"})
		return
	}

	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if du.Engine == "postgres" {
		_, err = h.cfg.Agent.Call(agentCtx, "db.postgres.grant", map[string]any{
			"db_name": db.Name,
			"role":    du.Username,
		})
	} else {
		_, err = h.cfg.Agent.Call(agentCtx, "db_user.grant", map[string]any{
			"db_name":      db.Name,
			"db_user_name": du.Username,
			"privileges":   strings.Split(canonicalPrivileges, ","),
		})
	}
	if err != nil {
		respondAgentErr(c, "agent_failed", err)
		return
	}

	now := time.Now().UTC()
	g := &models.DatabaseUserGrant{
		ID:             ids.NewULID(),
		DatabaseID:     db.ID,
		DatabaseUserID: du.ID,
		GrantLevel:     computedGrantLevel,
		Privileges:     canonicalPrivileges,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := h.cfg.DatabaseGrants.Create(ctx, g); err != nil {
		// Roll back the grant we just made (engine-aware).
		revokeCtx, revokeCancel := context.WithTimeout(ctx, 30*time.Second)
		defer revokeCancel()
		if du.Engine == "postgres" {
			_, _ = h.cfg.Agent.Call(revokeCtx, "db.postgres.revoke", map[string]any{
				"db_name": db.Name,
				"role":    du.Username,
			})
		} else {
			_, _ = h.cfg.Agent.Call(revokeCtx, "db_user.revoke", map[string]any{
				"db_name":      db.Name,
				"db_user_name": du.Username,
				"privileges":   strings.Split(canonicalPrivileges, ","),
			})
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.JSON(http.StatusCreated, grantResponse{
		ID:           g.ID,
		DatabaseID:   db.ID,
		DatabaseName: db.Name,
		GrantLevel:   g.GrantLevel,
		Privileges:   parsePrivilegesFromString(g.Privileges),
	})
}

// deleteGrant handles DELETE /database-user-grants/:id. Revokes a
// single grant, leaving the user and its other grants intact.
func (h *databaseUserHandler) deleteGrant(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	g, err := h.cfg.DatabaseGrants.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	du, err := h.cfg.DatabaseUsers.FindByID(ctx, g.DatabaseUserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && du.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	db, err := h.cfg.Databases.FindByID(ctx, g.DatabaseID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if du.Engine == "postgres" {
		_, err = h.cfg.Agent.Call(agentCtx, "db.postgres.revoke", map[string]any{
			"db_name": db.Name,
			"role":    du.Username,
		})
	} else {
		_, err = h.cfg.Agent.Call(agentCtx, "db_user.revoke", map[string]any{
			"db_name":      db.Name,
			"db_user_name": du.Username,
			"privileges":   strings.Split(g.Privileges, ","),
		})
	}
	if err != nil {
		respondAgentErr(c, "agent_failed", err)
		return
	}

	if err := h.cfg.DatabaseGrants.Delete(ctx, g.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *databaseUserHandler) delete(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Load the database user
	du, err := h.cfg.DatabaseUsers.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Check authorization
	if !claims.IsAdmin && du.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Load all grants for this user to revoke them
	grants, err := h.cfg.DatabaseGrants.ListByDatabaseUserID(ctx, du.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Revoke all grants
	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	for _, grant := range grants {
		// Load database to get name
		db, err := h.cfg.Databases.FindByID(ctx, grant.DatabaseID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}

		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		if du.Engine == "postgres" {
			_, err = h.cfg.Agent.Call(agentCtx, "db.postgres.revoke", map[string]any{
				"db_name": db.Name,
				"role":    du.Username,
			})
		} else {
			_, err = h.cfg.Agent.Call(agentCtx, "db_user.revoke", map[string]any{
				"db_name":      db.Name,
				"db_user_name": du.Username,
				"privileges":   strings.Split(grant.Privileges, ","),
			})
		}
		if err != nil {
			respondAgentErr(c, "agent_failed", err)
			return
		}
	}

	// Drop user from the engine it lives on.
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err = h.cfg.Agent.Call(agentCtx, dbUserCmd(du.Engine, "drop"), dbUserDropParams(du.Engine, du.Username))
	if err != nil {
		respondAgentErr(c, "agent_failed", err)
		return
	}

	// Delete grants and user in transaction
	// Delete all grants and user
	for _, grant := range grants {
		_ = h.cfg.DatabaseGrants.Delete(ctx, grant.ID)
	}

	if err := h.cfg.DatabaseUsers.Delete(ctx, du.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DBUserRotateAgentCommand returns the engine-correct agent verb + params for
// rotating a database user's password. Shared by the REST handler and the
// operator CLI so they can never drift on the wire contract (JAB-285): the
// MariaDB rotate command's field is new_password — the CLI had regressed to
// `password`, which the agent rejects as "new_password cannot be empty", so
// CLI MariaDB rotation always failed. PostgreSQL has no separate rotate verb;
// create_role's idempotent upsert ALTERs the password (field `password`).
func DBUserRotateAgentCommand(engine, username, newPassword string) (verb string, params map[string]any) {
	if engine == "postgres" {
		return "db.postgres.create_role", map[string]any{"role": username, "password": newPassword}
	}
	return "db_user.rotate_password", map[string]any{"db_user_name": username, "new_password": newPassword}
}

func (h *databaseUserHandler) rotatePassword(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req rotateDatabaseUserPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	// Load database user
	du, err := h.cfg.DatabaseUsers.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Check authorization
	if !claims.IsAdmin && du.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Generate new password
	plainPassword := ids.NewSecret()

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(plainPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Call agent to rotate password
	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	rotateVerb, rotateParams := DBUserRotateAgentCommand(du.Engine, du.Username, plainPassword)
	_, err = h.cfg.Agent.Call(agentCtx, rotateVerb, rotateParams)
	if err != nil {
		respondAgentErr(c, "agent_failed", err)
		return
	}

	// Update password hash in database
	if err := h.cfg.DatabaseUsers.UpdatePasswordHash(ctx, du.ID, string(hash)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.JSON(http.StatusOK, rotateDatabaseUserPasswordResponse{
		Password: plainPassword,
	})
}

func (h *databaseUserHandler) updateGrant(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req updateDatabaseUserGrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	// Determine privileges to use: either from privileges array or fallback to grant_level.
	var canonicalPrivileges string
	if len(req.Privileges) > 0 {
		// Validate and normalize privileges.
		var err error
		canonicalPrivileges, err = privilegesToCanonicalString(req.Privileges)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_privileges", "detail": err.Error()})
			return
		}
	} else if req.GrantLevel != "" {
		// Legacy path: translate grant_level to privileges.
		if req.GrantLevel == "rw" {
			canonicalPrivileges = "ALL"
		} else if req.GrantLevel == "ro" {
			canonicalPrivileges = "SELECT"
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant_level", "detail": "grant_level must be 'rw' or 'ro'"})
			return
		}
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_privileges", "detail": "either privileges or grant_level must be provided"})
		return
	}

	// Load grant
	grant, err := h.cfg.DatabaseGrants.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Load database user to check authorization
	du, err := h.cfg.DatabaseUsers.FindByID(ctx, grant.DatabaseUserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Check authorization
	if !claims.IsAdmin && du.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// No-op: same privileges
	if grant.Privileges == canonicalPrivileges {
		c.JSON(http.StatusOK, grantResponse{
			ID:           grant.ID,
			DatabaseID:   grant.DatabaseID,
			DatabaseName: "", // Will be fetched below
			GrantLevel:   grant.GrantLevel,
			Privileges:   parsePrivilegesFromString(grant.Privileges),
		})
		return
	}

	// Load database for name
	db, err := h.cfg.Databases.FindByID(ctx, grant.DatabaseID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Call agent to revoke old grant
	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if du.Engine == "postgres" {
		_, err = h.cfg.Agent.Call(agentCtx, "db.postgres.revoke", map[string]any{
			"db_name": db.Name,
			"role":    du.Username,
		})
	} else {
		_, err = h.cfg.Agent.Call(agentCtx, "db_user.revoke", map[string]any{
			"db_name":      db.Name,
			"db_user_name": du.Username,
			"grant_level":  grant.GrantLevel,
		})
	}
	if err != nil {
		respondAgentErr(c, "agent_failed", err)
		return
	}

	// Call agent to grant new privilege
	agentCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if du.Engine == "postgres" {
		_, err = h.cfg.Agent.Call(agentCtx, "db.postgres.grant", map[string]any{
			"db_name": db.Name,
			"role":    du.Username,
		})
	} else {
		_, err = h.cfg.Agent.Call(agentCtx, "db_user.grant", map[string]any{
			"db_name":      db.Name,
			"db_user_name": du.Username,
			"grant_level":  req.GrantLevel,
		})
	}
	if err != nil {
		// Restore-old-grant best-effort path. Mariadb path uses
		// privileges; PG path re-grants ALL.
		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if du.Engine == "postgres" {
			h.cfg.Agent.Call(agentCtx, "db.postgres.grant", map[string]any{
				"db_name": db.Name,
				"role":    du.Username,
			})
		} else {
			h.cfg.Agent.Call(agentCtx, "db_user.grant", map[string]any{
				"db_name":      db.Name,
				"db_user_name": du.Username,
				"privileges":   strings.Split(grant.Privileges, ","),
			})
		}
		respondAgentErr(c, "agent_failed", err)
		return
	}

	// Update privileges in database
	if err := h.cfg.DatabaseGrants.UpdatePrivileges(ctx, grant.ID, canonicalPrivileges); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Reload and return updated grant
	updatedGrant, err := h.cfg.DatabaseGrants.FindByID(ctx, grant.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.JSON(http.StatusOK, grantResponse{
		ID:           updatedGrant.ID,
		DatabaseID:   updatedGrant.DatabaseID,
		DatabaseName: db.Name,
		GrantLevel:   updatedGrant.GrantLevel,
		Privileges:   parsePrivilegesFromString(updatedGrant.Privileges),
	})
}

// ---- Validation helpers ----

func databaseUserNameValid(username string) bool {
	if len(username) == 0 || len(username) > 63 {
		return false
	}
	// ^[a-z][a-z0-9_]{0,63}$
	for i, ch := range username {
		if i == 0 {
			if ch < 'a' || ch > 'z' {
				return false
			}
		} else {
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_') {
				return false
			}
		}
	}
	return true
}
