// nginx.bots.apply — renders the http{}-scope fragment that tells the vhosts
// which requests come from a blocked user agent:
//
//	$jabali_ai_crawler   1 for the AI crawlers and AI assistants below, which
//	                     a domain blocks with its "Block AI crawlers" switch.
//	$jabali_blocked_ua   1 for the admin's server-wide blocked user agents.
//
// A vhost refers to them only when it needs them (BuildBotDirectives), so a
// host with no domain switched on and an empty list renders every vhost as
// before. A user-agent block only stops a client that says who it is; a bot
// that lies about its user agent is CrowdSec's job (scenarios, AppSec, the
// bot challenge).
//
// The fragment lives at /etc/nginx/conf.d/00-jabali-bots.conf. The 00- prefix
// loads it before any vhost that refers to the variables, as with
// 00-jabali-ratelimits.conf. Swapped in with the same stage → nginx -t →
// roll back sequence, then nginx is reloaded, because a changed list changes
// only this file and no vhost.
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/useragents"
)

const nginxBotsFragmentPath = "/etc/nginx/conf.d/00-jabali-bots.conf"

// nginxBotsFragment is a var so tests can point it at a temp dir.
var nginxBotsFragment = nginxBotsFragmentPath

// aiCrawlerUserAgents are user-agent tokens that AI companies publish for
// their crawlers and assistants. Only tokens sent in the User-Agent header:
// robots.txt-only tokens (Google-Extended, Applebot-Extended) never reach
// nginx and are left out.
var aiCrawlerUserAgents = []string{
	"AI2Bot",
	"Amazonbot",
	"anthropic-ai",
	"Bytespider",
	"CCBot",
	"ChatGPT-User",
	"Claude-SearchBot",
	"Claude-User",
	"ClaudeBot",
	"cohere-ai",
	"cohere-training-data-crawler",
	"Diffbot",
	"DuckAssistBot",
	"FacebookBot",
	"Google-CloudVertexBot",
	"GPTBot",
	"ImagesiftBot",
	"meta-externalagent",
	"meta-externalfetcher",
	"MistralAI-User",
	"OAI-SearchBot",
	"Omgilibot",
	"PanguBot",
	"Perplexity-User",
	"PerplexityBot",
	"Timpibot",
	"YouBot",
}

type nginxBotsApplyParams struct {
	BlockedUserAgents []string `json:"blocked_user_agents"`
}

type nginxBotsApplyResponse struct {
	FragmentPath string `json:"fragment_path"`
	BlockedCount int    `json:"blocked_count"`
	NoChange     bool   `json:"no_change,omitempty"`
}

func nginxBotsApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p nginxBotsApplyParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
		}
	}
	// The panel checked the list already; check again before it becomes
	// nginx config.
	blocked, err := useragents.Normalize(p.BlockedUserAgents)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "blocked user agents: " + err.Error()}
	}

	content := BuildNginxBotsFragment(blocked)
	resp := &nginxBotsApplyResponse{FragmentPath: nginxBotsFragment, BlockedCount: len(blocked)}
	if existing, err := os.ReadFile(nginxBotsFragment); err == nil && bytes.Equal(existing, []byte(content)) {
		resp.NoChange = true
		return resp, nil
	}

	// JAB-71: serialize stage→swap→test→reload against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	if err := swapNginxFragment(ctx, nginxBotsFragment, content, "nginx.bots"); err != nil {
		return nil, err
	}
	reload := execCommandContext(ctx, "systemctl", "reload", "nginx")
	var out bytes.Buffer
	reload.Stdout = &out
	reload.Stderr = &out
	if err := reload.Run(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl reload nginx failed: %s", out.String())}
	}
	return resp, nil
}

// swapNginxFragment puts content at path only if nginx -t passes with it,
// and otherwise leaves the previous file (or none) in place.
func swapNginxFragment(ctx context.Context, path, content, label string) error {
	tmpPath := path + ".new"
	backupPath := path + ".bak"
	if err := os.WriteFile(tmpPath, []byte(content), 0o644); err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write candidate fragment: %v", err)}
	}
	_ = os.Remove(backupPath)
	hadLive := false
	if _, err := os.Stat(path); err == nil {
		hadLive = true
		if err := os.Rename(path, backupPath); err != nil {
			_ = os.Remove(tmpPath)
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("back up live fragment: %v", err)}
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if hadLive {
			_ = os.Rename(backupPath, path)
		}
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("swap fragment: %v", err)}
	}
	test := execCommandContext(ctx, "nginx", "-t")
	var out bytes.Buffer
	test.Stdout = &out
	test.Stderr = &out
	if err := test.Run(); err != nil {
		_ = os.Remove(path)
		if hadLive {
			_ = os.Rename(backupPath, path)
		}
		return nginxTestFailure(label+" (rolled back)", out.String())
	}
	_ = os.Remove(backupPath)
	return nil
}

// ensureNginxBotsFragment writes the fragment with an empty blocked list when
// it does not exist yet, so a vhost that refers to the variables still passes
// nginx -t on a host where nginx.bots.apply has not run. The next apply
// writes the real list.
func ensureNginxBotsFragment() error {
	if _, err := os.Stat(nginxBotsFragment); err == nil {
		return nil
	}
	return os.WriteFile(nginxBotsFragment, []byte(BuildNginxBotsFragment(nil)), 0o644)
}

// BuildNginxBotsFragment renders the two maps. Entries are matched as
// case-insensitive substrings of the User-Agent header. Sorted and
// de-duplicated, so the same list always renders the same bytes.
func BuildNginxBotsFragment(blocked []string) string {
	var b strings.Builder
	b.WriteString("# Auto-generated by jabali reconciler — do not edit.\n")
	b.WriteString("# User agents the vhosts can refuse (see nginx.bots.apply).\n\n")
	writeUAMap(&b, "jabali_ai_crawler", aiCrawlerUserAgents)
	b.WriteString("\n")
	writeUAMap(&b, "jabali_blocked_ua", blocked)
	return b.String()
}

func writeUAMap(b *strings.Builder, variable string, entries []string) {
	seen := map[string]bool{}
	list := make([]string, 0, len(entries))
	for _, e := range entries {
		k := strings.ToLower(e)
		if e == "" || seen[k] {
			continue
		}
		seen[k] = true
		list = append(list, e)
	}
	sort.Strings(list)
	fmt.Fprintf(b, "map $http_user_agent $%s {\n    default 0;\n", variable)
	for _, e := range list {
		fmt.Fprintf(b, "    \"~*%s\" 1;\n", regexp.QuoteMeta(e))
	}
	b.WriteString("}\n")
}

// BuildBotDirectives returns the server-level lines that refuse the blocked
// user agents, or "" when the domain blocks nothing.
func BuildBotDirectives(blockAICrawlers, blockListedUserAgents bool) string {
	var b strings.Builder
	if blockAICrawlers {
		b.WriteString("    # This domain blocks AI crawlers that identify themselves.\n")
		b.WriteString("    if ($jabali_ai_crawler) { return 403; }\n")
	}
	if blockListedUserAgents {
		b.WriteString("    # User agents the admin blocked on every site.\n")
		b.WriteString("    if ($jabali_blocked_ua) { return 403; }\n")
	}
	return b.String()
}

func init() {
	Default.Register("nginx.bots.apply", nginxBotsApplyHandler)
}
