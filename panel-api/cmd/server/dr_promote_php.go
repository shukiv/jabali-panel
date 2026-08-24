package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// drAgentCaller is the slice of *agent.Client the promote convergence needs, so
// the PHP/version logic is unit-tested with a fake.
type drAgentCaller interface {
	Call(ctx context.Context, command string, params any) (json.RawMessage, error)
}

// GH #1169 gap 5 — converge required PHP versions + force pool re-apply at promote.
//
// A promoted standby is an EMPTY host carrying the old primary's DB. The
// replicated php_pools rows demand whatever PHP versions the tenants used and
// are marked status=active — but the reconciler skips active pools, so it never
// renders them onto the empty box, and a pool whose PHP version isn't installed
// would fail to render anyway. Promote therefore (1) installs every PHP version
// the pools require but the box lacks, then (2) flips active pools to pending so
// the reconciler re-renders them from the template on its next tick — the same
// primitive `jabali php pool reapply-all` uses (GH #401).

// missingPHPVersions returns the versions in required that are not installed,
// de-duplicated and sorted. Pure so the promote convergence is unit-tested
// without an agent or DB.
func missingPHPVersions(required, installed []string) []string {
	have := make(map[string]struct{}, len(installed))
	for _, v := range installed {
		have[v] = struct{}{}
	}
	seen := make(map[string]struct{})
	var out []string
	for _, v := range required {
		if v == "" {
			continue
		}
		if _, ok := have[v]; ok {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// markActivePoolsPending flips every active php_pools row to pending so the
// reconciler re-renders it from the current template. Shared by
// `jabali php pool reapply-all` (GH #401) and promote (GH #1169). Returns the
// count flipped.
func markActivePoolsPending(ctx context.Context, repo repository.PHPPoolRepository) (int, error) {
	pools, _, err := repo.ListAll(ctx, repository.ListOptions{Limit: 100000})
	if err != nil {
		return 0, fmt.Errorf("list pools: %w", err)
	}
	n := 0
	for i := range pools {
		if pools[i].Status != "active" {
			continue
		}
		if serr := repo.SetStatus(ctx, pools[i].ID, "pending", nil); serr != nil {
			return n, fmt.Errorf("mark pool %s pending: %w", pools[i].ID, serr)
		}
		n++
	}
	return n, nil
}

// promoteConvergePHP installs missing PHP versions the replicated pools require,
// then marks active pools pending for re-render. Best-effort on install (a box
// with no internet still promotes; the operator sees the warning and the pool
// stays pending until PHP is present), but a pool-repo error is fatal — the flip
// must not proceed on a half-known pool set.
func promoteConvergePHP(ctx context.Context, repo repository.PHPPoolRepository, agent drAgentCaller) error {
	pools, _, err := repo.ListAll(ctx, repository.ListOptions{Limit: 100000})
	if err != nil {
		return fmt.Errorf("list php pools: %w", err)
	}
	required := make([]string, 0, len(pools))
	for i := range pools {
		required = append(required, pools[i].PHPVersion)
	}

	installed, ierr := listInstalledPHPVersionsViaAgent(ctx, agent)
	if ierr != nil {
		fmt.Printf("  WARNING: could not list installed PHP versions (%v); skipping auto-install — "+
			"pools stay pending until their PHP version is present.\n", ierr)
	} else {
		for _, v := range missingPHPVersions(required, installed) {
			fmt.Printf("  installing missing PHP %s (required by a replicated pool)…\n", v)
			if _, aerr := agent.Call(ctx, "php.version.install", map[string]any{"version": v}); aerr != nil {
				fmt.Printf("    WARNING: php.version.install %s failed: %v — pool stays pending until installed.\n", v, aerr)
			}
		}
	}

	n, merr := markActivePoolsPending(ctx, repo)
	if merr != nil {
		return merr
	}
	fmt.Printf("  marked %d active pool(s) pending; the reconciler will render them onto this box.\n", n)
	return nil
}

// listInstalledPHPVersionsViaAgent calls php.version.list and returns the
// installed version strings.
func listInstalledPHPVersionsViaAgent(ctx context.Context, agent drAgentCaller) ([]string, error) {
	raw, err := agent.Call(ctx, "php.version.list", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Versions []string `json:"versions"`
	}
	if jerr := json.Unmarshal(raw, &out); jerr != nil {
		return nil, fmt.Errorf("parse php.version.list: %w", jerr)
	}
	return out.Versions, nil
}
