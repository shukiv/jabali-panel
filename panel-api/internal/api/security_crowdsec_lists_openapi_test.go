package api

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/blocklists"
)

// The spec lists the keys PUT .../blocklists/feeds accepts; they are the
// catalog's, in catalog order, so a list added to one is added to the other.
func TestCrowdsecListsOpenAPIEnumIsTheCatalog(t *testing.T) {
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(openAPIYAML, &doc))
	var v any = doc
	for _, k := range []string{"paths", "/admin/security/crowdsec/blocklists/feeds", "put", "requestBody",
		"content", "application/json", "schema", "properties", "enabled", "items", "enum"} {
		m, ok := v.(map[string]any)
		require.True(t, ok, "openapi.yaml: nothing at %q", k)
		v = m[k]
	}
	enum, ok := v.([]any)
	require.True(t, ok, "openapi.yaml: enabled.items.enum is not a list")
	got := make([]string, 0, len(enum))
	for _, e := range enum {
		got = append(got, fmt.Sprint(e))
	}
	want := make([]string, 0, len(blocklists.Catalog))
	for _, l := range blocklists.Catalog {
		want = append(want, l.Key)
	}
	require.Equal(t, want, got)
}
