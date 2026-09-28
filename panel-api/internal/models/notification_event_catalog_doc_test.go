package models

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var updateEventDoc = flag.Bool("update", false, "rewrite the generated event catalog in docs/site/admin/notifications-events.md")

const (
	eventDocPath  = "../../../docs/site/admin/notifications-events.md"
	eventDocBegin = "<!-- BEGIN generated event catalog: go test ./panel-api/internal/models -run TestNotificationEventCatalogDoc -update -->"
	eventDocEnd   = "<!-- END generated event catalog -->"
)

// renderEventCatalog renders AllNotificationEventKinds as the markdown table
// the admin docs publish.
func renderEventCatalog() string {
	var b strings.Builder
	b.WriteString("| Event | Severity | On by default | What it means |\n|---|---|---|---|\n")
	for _, k := range AllNotificationEventKinds {
		on := "no"
		if k.DefaultOn {
			on = "yes"
		}
		desc := strings.ReplaceAll(k.Label+". "+k.Description, "|", "\\|")
		b.WriteString("| `" + k.Kind + "` | " + k.Severity + " | " + on + " | " + desc + " |\n")
	}
	return b.String()
}

// TestNotificationEventCatalogDoc fails when the event catalog in the admin
// docs drifts from AllNotificationEventKinds. The doc listed 14 event names,
// none of which the dispatcher used, while the code had 64.
func TestNotificationEventCatalogDoc(t *testing.T) {
	raw, err := os.ReadFile(eventDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", eventDocPath, err)
	}
	doc := string(raw)
	start := strings.Index(doc, eventDocBegin)
	end := strings.Index(doc, eventDocEnd)
	if start < 0 || end < start {
		t.Fatalf("%s: missing the %q … %q markers", eventDocPath, eventDocBegin, eventDocEnd)
	}
	head := doc[:start+len(eventDocBegin)] + "\n\n"
	want := head + renderEventCatalog() + "\n" + doc[end:]
	if *updateEventDoc {
		if err := os.WriteFile(eventDocPath, []byte(want), 0o644); err != nil {
			t.Fatalf("write %s: %v", eventDocPath, err)
		}
		return
	}
	if doc != want {
		t.Errorf("the event catalog in %s drifted from AllNotificationEventKinds.\n"+
			"Regenerate: go test ./panel-api/internal/models -run TestNotificationEventCatalogDoc -update", eventDocPath)
	}
}
