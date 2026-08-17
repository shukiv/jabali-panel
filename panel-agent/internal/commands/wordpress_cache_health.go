package commands

// wordpress_cache_health.go — GH #605. Lightweight drift check for a
// cache-enabled WordPress install. `wp jabali-cache verify` exercises the whole
// stack end-to-end AS THE TENANT: it fails (non-zero exit) if the plugin is
// inactive (unknown command), the object-cache drop-in is missing/broken, the
// JABALI_CACHE_* constants are gone, the Redis ACL user/keyspace is wrong, or
// the socket is unreachable. So one call detects every drift mode the panel's
// cache_enabled=true flag silently lies about. Detection only — repair is the
// panel's idempotent SetApplicationCache path, driven by `jabali app cache-doctor`.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type cacheHealthParams struct {
	OSUser      string `json:"os_user"`
	InstallPath string `json:"install_path"`
	Host        string `json:"host,omitempty"` // GH #620: for the TTFB probe
}

func wordpressCacheHealthHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cacheHealthParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.OSUser == "" || p.InstallPath == "" {
		return nil, csInvalidArg("os_user and install_path are required")
	}

	out, err := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "jabali-cache", "verify")
	healthy := err == nil
	detail := strings.TrimSpace(out)
	if !healthy && detail == "" {
		detail = err.Error()
	}
	return map[string]any{"healthy": healthy, "detail": detail}, nil
}

// wordpressCacheStatsHandler (GH #617) returns Redis cache stats as JSON by
// running `wp jabali-cache stats` as the tenant (which holds the ACL creds).
func wordpressCacheStatsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cacheHealthParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.OSUser == "" || p.InstallPath == "" {
		return nil, csInvalidArg("os_user and install_path are required")
	}
	out, err := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "jabali-cache", "stats")
	if err != nil {
		return map[string]any{"connected": false, "detail": strings.TrimSpace(out)}, nil
	}
	var stats map[string]any
	if jErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &stats); jErr != nil {
		return map[string]any{"connected": false, "detail": "unparseable stats output"}, nil
	}
	return stats, nil
}

// wordpressCacheTrimHandler (JAB-59) enforces the per-install Redis object-cache
// budget by running `wp jabali-cache trim` as the tenant — the plugin holds the
// ACL creds and trims ONLY its own prefixed keyspace to max_keys derived from
// JABALI_CACHE_MAXMEMORY_MB. Without this handler the panel's post-warmup budget
// call was an unregistered no-op, so a busy install could fill shared Redis and
// evict other tenants (allkeys-lru). Returns {trimmed, budget_mb, max_keys}.
func wordpressCacheTrimHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cacheHealthParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.OSUser == "" || p.InstallPath == "" {
		return nil, csInvalidArg("os_user and install_path are required")
	}
	out, err := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "jabali-cache", "trim")
	if err != nil {
		return map[string]any{"ok": false, "trimmed": 0, "detail": strings.TrimSpace(out)}, nil
	}
	var res map[string]any
	if jErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); jErr != nil {
		return map[string]any{"ok": false, "trimmed": 0, "detail": "unparseable trim output"}, nil
	}
	res["ok"] = true
	return res, nil
}

// wordpressCacheProbeHandler (GH #620) returns the site's active plugins + WP
// version so the panel advisor can recommend a cache profile + settings.
func wordpressCacheProbeHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cacheHealthParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.OSUser == "" || p.InstallPath == "" {
		return nil, csInvalidArg("os_user and install_path are required")
	}
	pl, _ := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "plugin", "list", "--status=active", "--field=name", "--format=json")
	ver, _ := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "core", "version")
	var plugins []string
	_ = json.Unmarshal([]byte(strings.TrimSpace(pl)), &plugins)
	ttfbMs := 0
	hitVerified := false
	hitBlockCause, hitBlockEvidence := "", ""
	risky := []string{}
	host := strings.ToLower(strings.TrimSpace(p.Host))
	if host != "" && probeDomainRe.MatchString(host) {
		curlLocal := func(path, writeOut string) string {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			args := []string{"-kso", "/dev/null", "--max-time", "8", "--resolve", host + ":443:127.0.0.1"}
			if writeOut != "" {
				args = append(args, "-w", writeOut)
			}
			out, _ := execCommandContext(cctx, "curl", append(args, "https://"+host+path)...).Output()
			return strings.TrimSpace(string(out))
		}
		// Homepage TTFB (GH #620).
		if secs, err := strconv.ParseFloat(curlLocal("/", "%{time_starttransfer}"), 64); err == nil {
			ttfbMs = int(secs * 1000)
		}
		// Page-cache HIT verification: prime once, then read X-Jabali-Cache.
		_ = curlLocal("/", "")
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		// GET, not HEAD (-I): the nginx cache key embeds $request_method, so a
		// HEAD can never HIT the GET-primed entry and this probe always read MISS.
		hdr, _ := execCommandContext(cctx, "curl", "-ks", "-o", "/dev/null", "-D", "-", "--max-time", "8",
			"--resolve", host+":443:127.0.0.1", "https://"+host+"/").Output()
		cancel()
		hitVerified = strings.Contains(strings.ToUpper(string(hdr)), "X-JABALI-CACHE: HIT")
		// JAB-201: when the double-fetch does not verify, name the
		// blocker from the same response's headers instead of leaving
		// the advisor to guess "cookie/plugin or not warmed".
		if !hitVerified {
			hitBlockCause, hitBlockEvidence = classifyCacheBlock(string(hdr))
		}
		// Risky endpoints that must never be cached (WooCommerce/EDD/membership).
		for _, ep := range []string{"/cart", "/checkout", "/my-account"} {
			if code := curlLocal(ep, "%{http_code}"); code == "200" || code == "302" {
				risky = append(risky, ep)
			}
		}
	}
	// JAB-201: cache OUTCOMES, not just config presence — the jcache
	// log ratio and the purge-spool/jail assertions.
	logHits, logMisses, logBypass, logOther, logOK := 0, 0, 0, 0, false
	if host != "" && probeDomainRe.MatchString(host) {
		logHits, logMisses, logBypass, logOther, logOK = tallyJcacheLogFile(host)
	}
	spoolOK, spoolDetail := probeSpool(p.OSUser)
	return map[string]any{
		"active_plugins": plugins, "wp_version": strings.TrimSpace(ver),
		"ttfb_ms": ttfbMs, "page_hit_verified": hitVerified, "risky_endpoints": risky,
		"hit_block_cause": hitBlockCause, "hit_block_evidence": hitBlockEvidence,
		"log_ok": logOK, "log_hits": logHits, "log_misses": logMisses,
		"log_bypass": logBypass, "log_other": logOther,
		"spool_ok": spoolOK, "spool_detail": spoolDetail,
	}, nil
}

func init() {
	Default.Register("wordpress.cache_health", wordpressCacheHealthHandler)
	Default.Register("wordpress.cache_probe", wordpressCacheProbeHandler)
	Default.Register("wordpress.cache_stats", wordpressCacheStatsHandler)
	Default.Register("wordpress.cache_trim", wordpressCacheTrimHandler)
}
