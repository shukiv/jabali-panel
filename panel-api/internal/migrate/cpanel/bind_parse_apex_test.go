package cpanel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/agent"
)

// okAgent is a no-op AgentInterface that accepts every dns.zone.upsert
// call so ImportDNS reaches its apex-NS filter path.
type okAgent struct{}

func (okAgent) Call(_ context.Context, _ string, _ any) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

var _ agent.AgentInterface = okAgent{}

// TestImportDNS_ApexNSSkipEmittedOncePerZone is the bug-5 regression
// guard. A normal zone carries two apex NS records (ns1 + ns2). The
// filter must emit apex_ns_handled_by_pdns:<origin> ONCE per zone, not
// once per filtered record, or the manifest duplicates the line.
func TestImportDNS_ApexNSSkipEmittedOncePerZone(t *testing.T) {
	dir := t.TempDir()
	zonePath := filepath.Join(dir, "example.com.db")
	zone := `$ORIGIN example.com.
$TTL 14400
@   IN  SOA ns1.example.com. admin.example.com. ( 2026010101 3600 1800 1209600 86400 )
@       IN  NS  ns1.example.com.
@       IN  NS  ns2.example.com.
www     IN  A   192.0.2.10
@       IN  A   192.0.2.10
`
	if err := os.WriteFile(zonePath, []byte(zone), 0o644); err != nil {
		t.Fatal(err)
	}

	parsed := &ParsedTarball{ZoneFiles: []string{zonePath}}
	res, err := ImportDNS(context.Background(), okAgent{}, parsed)
	if err != nil {
		t.Fatalf("ImportDNS: %v", err)
	}
	n := 0
	for _, s := range res.Skipped {
		if strings.HasPrefix(s, "apex_ns_handled_by_pdns:") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("apex_ns_handled_by_pdns emitted %d times, want 1 (skipped=%v)", n, res.Skipped)
	}
}
