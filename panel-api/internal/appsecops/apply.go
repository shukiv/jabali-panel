// Package appsecops applies the operator-managed CrowdSec AppSec rules from the
// panel (GH #1650).
//
// The rows live in two tables: crs_rule_exclusions (JAB-227) and crs_host_modes
// (GH #1641). They render into one operator before-plugin file. panel-api runs
// as `jabali` (JAB-357) and cannot write that file or reload crowdsec, so Apply
// reads both tables and hands the complete set to the agent verb
// appseccfg.OperatorApplyVerb, which renders and writes it as root.
package appsecops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ExclusionStore is satisfied by repository.CRSRuleExclusionRepository.
type ExclusionStore interface {
	List(ctx context.Context) ([]models.CRSRuleExclusion, error)
	Create(ctx context.Context, e *models.CRSRuleExclusion) error
	DeleteByID(ctx context.Context, id string) error
}

// HostModeLister is satisfied by repository.CRSHostModeRepository.
type HostModeLister interface {
	List(ctx context.Context) ([]models.CRSHostMode, error)
}

// InstallFinder is satisfied by repository.ApplicationInstallRepository.
type InstallFinder interface {
	FindByID(ctx context.Context, id string) (*models.ApplicationInstall, error)
}

// DomainFinder is satisfied by repository.DomainRepository.
type DomainFinder interface {
	FindByID(ctx context.Context, id string) (*models.Domain, error)
}

// Deps are the stores and the agent the operations need. Apply needs Agent,
// Exclusions and HostModes; SyncFlarum also needs Installs and Domains.
type Deps struct {
	Agent      agent.AgentInterface
	Exclusions ExclusionStore
	HostModes  HostModeLister
	Installs   InstallFinder
	Domains    DomainFinder
}

// ErrNotConfigured means a dependency is missing, so nothing was attempted.
var ErrNotConfigured = errors.New("appsecops: not configured")

// mu serialises every read-rows-then-apply sequence in this process. Each
// apply sends the complete desired state, so two overlapping applies could
// otherwise land in the wrong order: the older snapshot would overwrite the
// newer one and drop a row from the live file.
var mu sync.Mutex

// Apply writes the operator before-plugin file from the current rows and
// reloads crowdsec if it changed. If either table cannot be read, nothing is
// sent and the live file stays as it is: a partial read must never remove the
// other table's live entries.
func Apply(ctx context.Context, d Deps) (appseccfg.OperatorApplyResult, error) {
	mu.Lock()
	defer mu.Unlock()
	return applyLocked(ctx, d)
}

func applyLocked(ctx context.Context, d Deps) (appseccfg.OperatorApplyResult, error) {
	if d.Agent == nil || d.Exclusions == nil || d.HostModes == nil {
		return appseccfg.OperatorApplyResult{}, ErrNotConfigured
	}
	rows, err := d.Exclusions.List(ctx)
	if err != nil {
		return appseccfg.OperatorApplyResult{}, fmt.Errorf("list CRS exclusions (live file left as-is): %w", err)
	}
	modes, err := d.HostModes.List(ctx)
	if err != nil {
		return appseccfg.OperatorApplyResult{}, fmt.Errorf("list CRS host modes (live file left as-is): %w", err)
	}

	list := make([]appseccfg.Exclusion, 0, len(rows))
	for _, r := range rows {
		list = append(list, appseccfg.Exclusion{Host: r.Host, URIPrefix: r.URIPrefix, RuleID: r.RuleID, Note: r.Note})
	}
	hm := make([]appseccfg.HostMode, 0, len(modes))
	for _, m := range modes {
		hm = append(hm, appseccfg.HostMode{Host: m.Host, Mode: m.Mode, Note: m.Note})
	}

	raw, err := d.Agent.Call(ctx, appseccfg.OperatorApplyVerb, appseccfg.NewOperatorApplyParams(list, hm))
	if err != nil {
		return appseccfg.OperatorApplyResult{}, fmt.Errorf("agent %s: %w", appseccfg.OperatorApplyVerb, err)
	}
	var res appseccfg.OperatorApplyResult
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &res); err != nil {
			return appseccfg.OperatorApplyResult{}, fmt.Errorf("parse %s result: %w", appseccfg.OperatorApplyVerb, err)
		}
	}
	return res, nil
}
