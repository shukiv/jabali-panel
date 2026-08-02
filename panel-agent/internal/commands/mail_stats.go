package commands

// mail.stats_sample — one sample of Stalwart's operational counters for the
// mail-statistics history (GH #873).
//
// Source is Stalwart's Prometheus exporter (GET /metrics/prometheus on the
// local admin listener). The exporter ships DISABLED; on first sample this
// handler enables it via the x:Metrics registry singleton with a dedicated
// generated Basic-auth credential and restarts jabali-stalwart once (the
// exporter route only mounts at boot — probe-verified on 0.16.15).
//
// Auth is NOT optional: 8446 binds 127.0.0.1, but on a shared-hosting box
// every tenant process can reach 127.0.0.1 — an unauthenticated exporter
// would leak server-wide mail telemetry to tenants. The credential lives in
// a root-only file and never leaves the box.
//
// The sample is name-agnostic: every counter and gauge the exporter emits
// is returned as-is (Stalwart registers metrics lazily on first event, so
// the set grows as traffic flows). The panel stores what it gets; naming
// is resolved at display time.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const stalwartMetricsUser = "jabali-metrics"

// stalwartMetricsSecretPathFunc is overridable for tests.
var stalwartMetricsSecretPathFunc = func() string {
	return "/etc/jabali-panel/stalwart-metrics.secret"
}

type mailStatsResponse struct {
	// Metrics carries every counter and gauge from the exporter, verbatim.
	Metrics map[string]float64 `json:"metrics"`
	// QueueSize is the current QueuedMessage count.
	QueueSize int64 `json:"queue_size"`
	// EnabledNow is true when this call had to enable the exporter (and
	// restart Stalwart) first.
	EnabledNow bool `json:"enabled_now"`
}

func mailStatsSampleHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	metrics, enabledNow, err := scrapeStalwartMetrics(ctx)
	if err != nil {
		return nil, err
	}

	var queueResult jmapQueryResult
	if err := jmapCall(ctx, "x:QueuedMessage/query", map[string]any{"limit": 1}, &queueResult); err != nil {
		return nil, err
	}

	return mailStatsResponse{
		Metrics:    metrics,
		QueueSize:  int64(queueResult.Total),
		EnabledNow: enabledNow,
	}, nil
}

// scrapeStalwartMetrics fetches and parses the exporter output, enabling
// the exporter first when needed.
func scrapeStalwartMetrics(ctx context.Context) (map[string]float64, bool, error) {
	secret, haveSecret := readMetricsSecret()
	if haveSecret {
		if m, code, err := fetchMetrics(ctx, secret); err == nil && code == http.StatusOK {
			return m, false, nil
		} else if err != nil {
			return nil, false, err
		}
		// 404 (exporter off) or 401 (secret drift): fall through and
		// (re-)converge the exporter config below.
	}
	secret, err := ensureMetricsExporter(ctx)
	if err != nil {
		return nil, false, err
	}
	// The route mounts at boot — poll briefly after the restart.
	deadline := time.Now().Add(30 * time.Second)
	for {
		m, code, err := fetchMetrics(ctx, secret)
		if err == nil && code == http.StatusOK {
			return m, true, nil
		}
		if time.Now().After(deadline) {
			return nil, false, &agentwire.AgentError{
				Code:    agentwire.CodeUnavailable,
				Message: fmt.Sprintf("stalwart metrics endpoint not up after enable (last status %d, err %v)", code, err),
			}
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func readMetricsSecret() (string, bool) {
	b, err := os.ReadFile(stalwartMetricsSecretPathFunc())
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(b))
	return s, s != ""
}

// ensureMetricsExporter generates (or re-reads) the scrape credential,
// writes the registry singleton, and restarts Stalwart so the exporter
// route mounts.
func ensureMetricsExporter(ctx context.Context) (string, error) {
	secret, ok := readMetricsSecret()
	if !ok {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("generate metrics secret: %v", err)}
		}
		secret = hex.EncodeToString(raw)
		if err := os.WriteFile(stalwartMetricsSecretPathFunc(), []byte(secret+"\n"), 0o600); err != nil {
			return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write metrics secret: %v", err)}
		}
	}

	args := map[string]any{
		"update": map[string]any{
			"singleton": map[string]any{
				"prometheus": map[string]any{
					"@type":        "Enabled",
					"authUsername": stalwartMetricsUser,
					"authSecret":   map[string]any{"@type": "Value", "secret": secret},
				},
			},
		},
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:Metrics/set", args, &result); err != nil {
		return "", err
	}

	if out, err := runSystemctl(ctx, "restart", "jabali-stalwart.service"); err != nil {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("restart jabali-stalwart after metrics enable: %v: %s", err, strings.TrimSpace(string(out))),
		}
	}
	return secret, nil
}

// fetchMetrics GETs the exporter and parses counters + gauges. Histogram
// series are skipped — the panel's rollup wants monotonic counters and
// point-in-time gauges, not bucket vectors.
func fetchMetrics(ctx context.Context, secret string) (map[string]float64, int, error) {
	url := stalwartAdminURLFunc() + "/metrics/prometheus"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("build metrics request: %v", err)}
	}
	req.SetBasicAuth(stalwartMetricsUser, secret)
	resp, err := stalwartHTTPClientFunc().Do(req)
	if err != nil {
		return nil, 0, &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: fmt.Sprintf("stalwart metrics unreachable: %v", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return nil, resp.StatusCode, nil
	}
	m, perr := parsePrometheusText(resp.Body)
	if perr != nil {
		return nil, resp.StatusCode, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("parse metrics: %v", perr)}
	}
	return m, resp.StatusCode, nil
}

// parsePrometheusText reads the exposition format, keeping counter and
// gauge samples and dropping histograms (and their _bucket/_sum/_count
// series). Labelled samples keep the bare name and sum across label sets.
func parsePrometheusText(r io.Reader) (map[string]float64, error) {
	keep := map[string]bool{}
	out := map[string]float64{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			fields := strings.Fields(line)
			if len(fields) == 4 {
				keep[fields[2]] = fields[3] == "counter" || fields[3] == "gauge"
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		sp := strings.LastIndexByte(line, ' ')
		if sp <= 0 {
			continue
		}
		name, valStr := line[:sp], line[sp+1:]
		if i := strings.IndexByte(name, '{'); i > 0 {
			name = name[:i]
		}
		name = strings.TrimSpace(name)
		if !keep[name] {
			continue
		}
		v, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}
		out[name] += v
	}
	return out, sc.Err()
}

func init() {
	Default.Register("mail.stats_sample", mailStatsSampleHandler)
}
