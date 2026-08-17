package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
)

// nginx_cache_capacity.go — GH #634. Rewrite the shared FastCGI cache zone's
// capacity (keys_zone size, max_size, inactive) in the managed
// jabali-fastcgi-cache.conf, validate, and reload nginx — restoring the prior
// conf if `nginx -t` fails so a bad value never leaves nginx unable to reload.

const jabaliFcgiConfPath = "/etc/nginx/conf.d/jabali-fastcgi-cache.conf"

type nginxCacheCapacityParams struct {
	MaxSizeGB   int `json:"max_size_gb"`
	KeyzoneMB   int `json:"keyzone_mb"`
	InactiveMin int `json:"inactive_min"`
}

var (
	fcgiKeyzoneRE  = regexp.MustCompile(`keys_zone=jabali_fcgi:\d+m`)
	fcgiMaxSizeRE  = regexp.MustCompile(`max_size=\d+g`)
	fcgiInactiveRE = regexp.MustCompile(`inactive=\d+m`)
)

func nginxCacheCapacityApplyHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p nginxCacheCapacityParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, csInvalidArg("bad params")
	}
	if p.MaxSizeGB < 1 || p.MaxSizeGB > 1024 {
		return nil, csInvalidArg("max_size_gb must be 1..1024")
	}
	if p.KeyzoneMB < 8 || p.KeyzoneMB > 1024 {
		return nil, csInvalidArg("keyzone_mb must be 8..1024")
	}
	if p.InactiveMin < 1 || p.InactiveMin > 1440 {
		return nil, csInvalidArg("inactive_min must be 1..1440")
	}

	// JAB-71: serialize read→write→test→reload against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	orig, err := os.ReadFile(jabaliFcgiConfPath)
	if err != nil {
		return nil, csInternal("read cache conf", err)
	}
	updated := string(orig)
	updated = fcgiKeyzoneRE.ReplaceAllString(updated, "keys_zone=jabali_fcgi:"+strconv.Itoa(p.KeyzoneMB)+"m")
	updated = fcgiMaxSizeRE.ReplaceAllString(updated, "max_size="+strconv.Itoa(p.MaxSizeGB)+"g")
	updated = fcgiInactiveRE.ReplaceAllString(updated, "inactive="+strconv.Itoa(p.InactiveMin)+"m")
	if updated == string(orig) {
		return map[string]any{"ok": true, "changed": false}, nil
	}
	if err := os.WriteFile(jabaliFcgiConfPath, []byte(updated), 0o644); err != nil {
		return nil, csInternal("write cache conf", err)
	}
	// Validate; restore the prior conf on failure so nginx stays reloadable.
	if out, terr := execCommandContext(ctx, "nginx", "-t").CombinedOutput(); terr != nil {
		_ = os.WriteFile(jabaliFcgiConfPath, orig, 0o644)
		logNginxTestFailure("nginx.cache_capacity (reverted)", string(out))
		return nil, csInvalidArg("nginx rejected the new cache capacity (reverted)")
	}
	if out, rerr := execCommandContext(ctx, "systemctl", "reload", "nginx").CombinedOutput(); rerr != nil {
		return nil, csInternal("nginx reload", fmt.Errorf("%v: %s", rerr, string(out)))
	}
	return map[string]any{"ok": true, "changed": true,
		"max_size_gb": p.MaxSizeGB, "keyzone_mb": p.KeyzoneMB, "inactive_min": p.InactiveMin}, nil
}

func init() {
	Default.Register("nginx.cache.capacity_apply", nginxCacheCapacityApplyHandler)
}
