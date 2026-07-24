package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// scanFakeAgent returns a canned docker_app.log_scan result.
type scanFakeAgent struct{ result string }

func (a *scanFakeAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	if cmd == "docker_app.log_scan" {
		return json.RawMessage(a.result), nil
	}
	return json.RawMessage(`{}`), nil
}

// captureHandler records slog records so a test can assert what the scanner
// warned about.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) warnDirs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var dirs []string
	for _, rec := range h.records {
		if rec.Level != slog.LevelWarn {
			continue
		}
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == "dir" {
				dirs = append(dirs, a.Value.String())
			}
			return true
		})
	}
	return dirs
}

func (h *captureHandler) warnMsgContains(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if rec.Level == slog.LevelWarn && strings.Contains(rec.Message, sub) {
			return true
		}
	}
	return false
}

func has(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// JAB-121 phase 3: a log-holding dir with no catalog rotate policy is warned as
// unmanaged; a declared dir (flarum storagelogs) below the size threshold and a
// non-log dir are not; the scan runs once per boot.
func TestScanLogs_WarnsUnmanaged(t *testing.T) {
	cap := &captureHandler{}
	ag := &scanFakeAgent{result: `{"entries":[
		{"name":"storagelogs","bytes":100,"has_logs":true},
		{"name":"mystery-logs","bytes":500,"has_logs":true},
		{"name":"db","bytes":99999,"has_logs":false}
	]}`}
	r := &Reconciler{agent: ag, dockerCatalog: loadReconcileTestCatalog(t), log: slog.New(cap)}
	app := &models.DockerApp{ID: "a1", Slug: "flarum"}

	r.scanLogs(context.Background(), app)
	dirs := cap.warnDirs()
	if !has(dirs, "mystery-logs") {
		t.Errorf("expected unmanaged warn for mystery-logs, warned dirs=%v", dirs)
	}
	if has(dirs, "storagelogs") {
		t.Errorf("declared+small storagelogs should not warn, warned dirs=%v", dirs)
	}
	if has(dirs, "db") {
		t.Errorf("non-log dir should not warn, warned dirs=%v", dirs)
	}

	before := len(cap.records)
	r.scanLogs(context.Background(), app)
	if len(cap.records) != before {
		t.Errorf("second scan should be a no-op (once per boot)")
	}
}

// A DECLARED log dir that is huge (rotation not keeping up) is warned as oversized.
func TestScanLogs_WarnsOversizedDeclared(t *testing.T) {
	cap := &captureHandler{}
	ag := &scanFakeAgent{result: fmt.Sprintf(`{"entries":[{"name":"storagelogs","bytes":%d,"has_logs":true}]}`, int64(2)<<30)}
	r := &Reconciler{agent: ag, dockerCatalog: loadReconcileTestCatalog(t), log: slog.New(cap)}
	r.scanLogs(context.Background(), &models.DockerApp{ID: "a2", Slug: "flarum"})
	if !cap.warnMsgContains("oversized") {
		t.Errorf("expected oversized warn for a 2 GiB declared log dir")
	}
}
