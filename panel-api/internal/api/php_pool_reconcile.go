package api

import (
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/phppoolops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcilePHPPoolViaAgent fires php.pool.apply for the given pool and writes
// the resulting status back to the DB. It delegates to
// phppoolops.ReconcileViaAgent so the HTTP handlers and the operator CLI drive
// the identical apply (slug/additive + the full pm.* + slowlog + extensions +
// Xdebug model); see that package for the payload.
//
// pool is taken BY VALUE: callers that spawn it with `go` pass a copy, so the
// fire-and-forget reconcile cannot race a handler that keeps mutating its own
// pool (JAB-360 removes that repo-wide `-race` flake). Callers that need the
// resulting status (the synchronous opcache path) read it back from the DB.
//
// Used by the admin /php-pools/:id paths and the user-driven
// /domains/:id/php-pool path so the flows behave identically.
func reconcilePHPPoolViaAgent(
	ag agent.AgentInterface,
	users repository.UserRepository,
	overrides repository.PHPPoolIniOverrideRepository,
	pools repository.PHPPoolRepository,
	pool models.PHPPool,
) {
	_ = phppoolops.ReconcileViaAgent(phppoolops.ReconcileDeps{
		Agent:     ag,
		Users:     users,
		Overrides: overrides,
		Pools:     pools,
	}, pool)
}
