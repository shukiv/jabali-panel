package commands

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const sampleExposition = `# HELP smtp_active_connections x
# TYPE smtp_active_connections gauge
smtp_active_connections 3
# TYPE message_ingest counter
message_ingest{listener="smtp"} 10
message_ingest{listener="lmtp"} 5
# TYPE http_request_time histogram
http_request_time_bucket{le="0.5"} 100
http_request_time_sum 12.5
http_request_time_count 100
# TYPE domain_count gauge
domain_count 4
`

func TestParsePrometheusText(t *testing.T) {
	m, err := parsePrometheusText(strings.NewReader(sampleExposition))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m["smtp_active_connections"] != 3 {
		t.Fatalf("gauge = %v", m["smtp_active_connections"])
	}
	if m["message_ingest"] != 15 {
		t.Fatalf("labelled counter must sum across label sets, got %v", m["message_ingest"])
	}
	if m["domain_count"] != 4 {
		t.Fatalf("domain_count = %v", m["domain_count"])
	}
	for _, histo := range []string{"http_request_time", "http_request_time_bucket", "http_request_time_sum", "http_request_time_count"} {
		if _, ok := m[histo]; ok {
			t.Fatalf("histogram series %q must be dropped", histo)
		}
	}
}

// Stalwart 1.0's exporter, as 1.0.2 writes it: "stalwart_" on every name,
// "_total" on counters, and domains/users/server_memory_bytes renamed.
const sampleExposition10 = `# HELP stalwart_auth_success_total Authentication successful
# TYPE stalwart_auth_success_total counter
stalwart_auth_success_total 1
# TYPE stalwart_message_ingest_ham_total counter
stalwart_message_ingest_ham_total 7
# TYPE stalwart_queue_authenticated_message_queued_total counter
stalwart_queue_authenticated_message_queued_total 2
# TYPE stalwart_delivery_completed_total counter
stalwart_delivery_completed_total 9
# TYPE stalwart_server_memory_bytes gauge
stalwart_server_memory_bytes 102477824
# TYPE stalwart_users gauge
stalwart_users 23
# TYPE stalwart_domains gauge
stalwart_domains 74
# TYPE stalwart_smtp_active_connections gauge
stalwart_smtp_active_connections 4
# TYPE stalwart_imap_request_time_seconds histogram
stalwart_imap_request_time_seconds_bucket{le="0.5"} 3
stalwart_imap_request_time_seconds_sum 0.2
stalwart_imap_request_time_seconds_count 3
`

// The panel stores and reads 0.16's names (MailStatsTab, the weekly digest),
// so a 1.0 sample must come out under the same names.
func TestParsePrometheusText_Stalwart10Names(t *testing.T) {
	m, err := parsePrometheusText(strings.NewReader(sampleExposition10))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]float64{
		"auth_success":                       1,
		"message_ingest_ham":                 7,
		"queue_authenticated_message_queued": 2,
		"delivery_completed":                 9,
		"server_memory":                      102477824,
		"user_count":                         23,
		"domain_count":                       74,
		"smtp_active_connections":            4,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if len(m) != len(want) {
		t.Errorf("got %d series %v, want %d (histograms dropped, no stalwart_ names left)", len(m), m, len(want))
	}
}

// statsTestServer speaks both /jmap and /metrics/prometheus. The exporter
// starts "off" (404) when enabledAtStart is false and turns on after a
// jabali-stalwart restart, mirroring the real mount-at-boot behaviour.
func newStatsTestServer(t *testing.T, secret string, enabledAtStart bool) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	exporterUp := &atomic.Int32{}
	if enabledAtStart {
		exporterUp.Store(1)
	}
	metricsSet := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics/prometheus":
			if exporterUp.Load() == 0 {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			u, p, ok := r.BasicAuth()
			if !ok || u != stalwartMetricsUser || p != secret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(sampleExposition)) //nolint:errcheck
		case jmapAPIPath:
			var req jmapRequestBody
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &req); err != nil || len(req.MethodCalls) != 1 {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			var result any
			switch req.MethodCalls[0].Name {
			case "x:QueuedMessage/query":
				result = map[string]any{"ids": []string{"q1"}, "total": 7}
			case "x:Metrics/set":
				metricsSet.Add(1)
				result = map[string]any{"updated": map[string]any{"singleton": nil}}
			default:
				http.Error(w, "no route "+req.MethodCalls[0].Name, http.StatusBadRequest)
				return
			}
			resp := jmapResponseBody{MethodResponses: []jmapMethodCall{{
				Name: req.MethodCalls[0].Name, Args: result, CallID: req.MethodCalls[0].CallID,
			}}}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp) //nolint:errcheck
		default:
			http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, exporterUp, metricsSet
}

func wireStatsSecret(t *testing.T, secret string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.secret")
	if secret != "" {
		if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	orig := stalwartMetricsSecretPathFunc
	stalwartMetricsSecretPathFunc = func() string { return path }
	t.Cleanup(func() { stalwartMetricsSecretPathFunc = orig })
}

// Steady state: exporter on, secret on disk — one scrape, no enable, no
// restart, queue total carried through.
func TestMailStatsSample_SteadyState(t *testing.T) {
	server, _, metricsSet := newStatsTestServer(t, "s3cret", true)
	wireJMAP(t, server)
	wireStatsSecret(t, "s3cret")
	origRestart := runSystemctl
	restarts := 0
	runSystemctl = func(ctx context.Context, args ...string) ([]byte, error) { restarts++; return nil, nil }
	t.Cleanup(func() { runSystemctl = origRestart })

	out, err := mailStatsSampleHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	resp := out.(mailStatsResponse)
	if resp.EnabledNow || restarts != 0 || metricsSet.Load() != 0 {
		t.Fatalf("steady state must not touch config (enabledNow=%v restarts=%d sets=%d)", resp.EnabledNow, restarts, metricsSet.Load())
	}
	if resp.QueueSize != 7 || resp.Metrics["message_ingest"] != 15 {
		t.Fatalf("resp = %+v", resp)
	}
}

// First run: exporter off, no secret — generate secret (0600), enable via
// x:Metrics/set, restart once, then scrape succeeds.
func TestMailStatsSample_EnablesExporterOnce(t *testing.T) {
	var server *httptest.Server
	var exporterUp *atomic.Int32
	var metricsSet *atomic.Int32
	// Secret is generated by the handler; the fake must accept whatever
	// lands in the secret file. Wire with a placeholder, then swap the
	// fake's expectation dynamically via file read inside auth check —
	// easiest is to run the enable, then re-read the file for asserts.
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.secret")
	orig := stalwartMetricsSecretPathFunc
	stalwartMetricsSecretPathFunc = func() string { return path }
	t.Cleanup(func() { stalwartMetricsSecretPathFunc = orig })

	exporterUp = &atomic.Int32{}
	metricsSet = &atomic.Int32{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics/prometheus":
			if exporterUp.Load() == 0 {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			u, p, ok := r.BasicAuth()
			fileSecret, _ := os.ReadFile(path)
			if !ok || u != stalwartMetricsUser || p != strings.TrimSpace(string(fileSecret)) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(sampleExposition)) //nolint:errcheck
		case jmapAPIPath:
			var req jmapRequestBody
			raw, _ := io.ReadAll(r.Body)
			json.Unmarshal(raw, &req) //nolint:errcheck
			var result any
			switch req.MethodCalls[0].Name {
			case "x:QueuedMessage/query":
				result = map[string]any{"ids": []string{}, "total": 0}
			case "x:Metrics/set":
				metricsSet.Add(1)
				result = map[string]any{"updated": map[string]any{"singleton": nil}}
			default:
				http.Error(w, "no route", http.StatusBadRequest)
				return
			}
			resp := jmapResponseBody{MethodResponses: []jmapMethodCall{{
				Name: req.MethodCalls[0].Name, Args: result, CallID: req.MethodCalls[0].CallID,
			}}}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp) //nolint:errcheck
		}
	}))
	t.Cleanup(server.Close)
	wireJMAP(t, server)

	origRestart := runSystemctl
	restarts := 0
	runSystemctl = func(ctx context.Context, args ...string) ([]byte, error) {
		restarts++
		exporterUp.Store(1) // exporter mounts on restart
		return nil, nil
	}
	t.Cleanup(func() { runSystemctl = origRestart })

	out, err := mailStatsSampleHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	resp := out.(mailStatsResponse)
	if !resp.EnabledNow || restarts != 1 || metricsSet.Load() != 1 {
		t.Fatalf("enable path wrong (enabledNow=%v restarts=%d sets=%d)", resp.EnabledNow, restarts, metricsSet.Load())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("secret file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode = %v, want 0600", info.Mode().Perm())
	}
}
