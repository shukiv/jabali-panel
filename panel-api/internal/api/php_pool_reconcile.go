package api

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcilePHPPoolViaAgent fires php.pool.apply against the agent for the
// given pool, then writes the resulting status back to the DB. Designed
// to be called inside `go ...` from request handlers so a user-driven
// version/config change converges immediately instead of waiting for the
// next reconciler tick.
//
// Used by both the admin /php-pools/:id PUT path and the user-driven
// POST /domains/:id/php-pool path so the two flows behave identically.
// Callers are expected to have already flipped pool.Status to "pending"
// (or its equivalent) before invoking — the helper itself overwrites
// status with "ready" on success or "error" on failure.
func reconcilePHPPoolViaAgent(
	ag agent.AgentInterface,
	users repository.UserRepository,
	overrides repository.PHPPoolIniOverrideRepository,
	pools repository.PHPPoolRepository,
	pool *models.PHPPool,
) {
	agentCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user, err := users.FindByID(agentCtx, pool.UserID)
	if err != nil {
		slog.ErrorContext(agentCtx, "reconcilePHPPoolViaAgent: load user", "error", err, "pool_id", pool.ID)
		return
	}

	overridesList, err := overrides.ListByPool(agentCtx, pool.ID)
	if err != nil {
		slog.ErrorContext(agentCtx, "reconcilePHPPoolViaAgent: list overrides", "error", err, "pool_id", pool.ID)
		return
	}

	adminValues := []map[string]string{}
	adminFlags := []map[string]string{}
	for _, override := range overridesList {
		kv := map[string]string{"name": override.Directive, "value": override.Value}
		if override.Kind == "flag" {
			adminFlags = append(adminFlags, kv)
		} else {
			adminValues = append(adminValues, kv)
		}
	}

	// GH #329: resolve the pool's slug so a versioned pool applies to its own
	// socket/instance rather than the default per-user one. isDefault = the
	// pool is the user's earliest (created_at ASC).
	username := ""
	if user.Username != nil {
		username = *user.Username
	}
	isDefault := true
	if list, lerr := pools.ListByUserID(agentCtx, pool.UserID); lerr == nil && len(list) > 0 {
		isDefault = list[0].ID == pool.ID
	}
	slug := models.PoolSlug(username, pool.PHPVersion, isDefault)

	_, err = ag.Call(agentCtx, "php.pool.apply", map[string]any{
		"username":                          username,
		"slug":                              slug,
		"additive":                          !isDefault,
		"php_version":                       pool.PHPVersion,
		"pm_mode":                           pool.PmMode,
		"pm_max_children":                   pool.PmMaxChildren,
		"process_idle_timeout_seconds":      pool.ProcessIdleTimeoutSeconds,
		"pm_start_servers":                  pool.PmStartServers,
		"pm_min_spare_servers":              pool.PmMinSpareServers,
		"pm_max_spare_servers":              pool.PmMaxSpareServers,
		"pm_max_requests":                   pool.PmMaxRequests,
		"request_terminate_timeout_seconds": pool.RequestTerminateTimeoutSeconds,
		"slowlog_timeout_seconds":           pool.SlowlogTimeoutSeconds,
		"admin_values":                      adminValues,
		"admin_flags":                       adminFlags,
		"extra_extensions":                  []string(pool.ExtraExtensions),
	})
	if err != nil {
		pool.Status = "error"
		errMsg := fmt.Sprintf("agent failed: %v", err)
		pool.LastError = &errMsg
		_ = pools.Update(agentCtx, pool)
		return
	}
	pool.Status = "ready"
	pool.LastError = nil
	_ = pools.Update(agentCtx, pool)
}
