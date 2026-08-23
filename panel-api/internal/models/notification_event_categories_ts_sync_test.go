package models

import (
	"os"
	"strings"
	"testing"
)

// TestNotificationEventCategoriesTSInSync is a cross-boundary contract test
// (JAB-381): every notification event kind in the Go catalog
// AllNotificationEventKinds must appear in the panel-ui category map
// (eventCategories.ts EVENT_KIND_CATEGORY). If a kind is added on the Go side
// but not mapped, it silently falls into the UI's "other" fallback group — this
// test fails CI first so the mapping is kept deliberate. Mirrors the panel↔agent
// contract-test discipline.
func TestNotificationEventCategoriesTSInSync(t *testing.T) {
	const tsPath = "../../../panel-ui/src/shells/admin/notifications/eventCategories.ts"

	data, err := os.ReadFile(tsPath)
	if err != nil {
		// Go-only checkout / panel-ui absent — nothing to assert against.
		t.Skipf("panel-ui category map not readable (%v) — skipping cross-boundary check", err)
	}
	content := string(data)

	// Isolate the EVENT_KIND_CATEGORY object so we don't match kinds that only
	// appear in a comment elsewhere in the file.
	start := strings.Index(content, "EVENT_KIND_CATEGORY")
	if start < 0 {
		t.Fatalf("eventCategories.ts does not define EVENT_KIND_CATEGORY")
	}
	mapBody := content[start:]

	var missing []string
	for _, meta := range AllNotificationEventKinds {
		// Keys are written as `"<kind>": "<category>",`.
		if !strings.Contains(mapBody, `"`+meta.Kind+`":`) {
			missing = append(missing, meta.Kind)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("these event kinds are missing from panel-ui eventCategories.ts EVENT_KIND_CATEGORY "+
			"(they would fall into the 'other' group) — add each to exactly one category: %v", missing)
	}
}
