package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// php_opcache_reset.go — GH #1332 item 10. Reset a pool's OPcache.
//
// OPcache's SHM lives in the FPM master process, so the only clean, complete
// reset is a RESTART of that master (a graceful USR2 reload keeps the SHM). The
// blast radius is exactly one user's sites on one PHP version — the slug's own
// jabali-fpm@<slug> master. A tenant can already call opcache_reset() from any
// PHP file they host, so this is a convenience, not a privileged capability.

type phpOpcacheResetParams struct {
	Username string `json:"username"`
	Slug     string `json:"slug"`
}

func phpOpcacheResetHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p phpOpcacheResetParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "malformed JSON body"}
	}
	if !phpPoolUsernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid username format"}
	}
	// The slug becomes a systemd instance name — validate as a safe path/instance
	// component (default pool: slug == username; versioned: "<user>-php<ver>").
	if !phpPoolSlugRegex.MatchString(p.Slug) || strings.Contains(p.Slug, "..") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid slug format"}
	}
	// Defense in depth: the slug must belong to this user, so a tenant can never
	// restart another user's FPM master by crafting a slug.
	if p.Slug != p.Username && !strings.HasPrefix(p.Slug, p.Username+"-php") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "slug does not belong to username"}
	}
	service := fmt.Sprintf("jabali-fpm@%s.service", p.Slug)
	// Same test escape hatch the pool-apply reload path uses.
	if os.Getenv("JABALI_PHP_POOL_SKIP_RELOAD") != "" {
		return map[string]any{"restarted": service, "skipped": true}, nil
	}
	if err := execCommandContext(ctx, "systemctl", "restart", service).Run(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("failed to restart %s: %v", service, err)}
	}
	return map[string]any{"restarted": service}, nil
}

func init() {
	Default.Register("php.opcache.reset", phpOpcacheResetHandler)
}
