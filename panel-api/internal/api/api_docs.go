// API documentation endpoint — serves the hand-curated OpenAPI 3 spec
// at /api/v1/_meta/openapi.json (also accepts .yaml for raw form).
//
// The spec lives at docs/api/openapi.yaml in the repo and is embedded
// into the binary via //go:embed, so a release-tarball install ships
// the docs along with the code. Updating the spec is a normal PR
// against that file.
//
// The panel UI fetches the JSON form and feeds it into a Redoc
// component on the "API Docs" page (admin + tenant shells both
// mount it; the spec describes every public endpoint, gated by
// security definitions so Redoc tags admin-only routes accordingly).

package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

//go:embed openapi.yaml
var openAPIYAML []byte

// RegisterAPIDocsRoutes mounts the spec at the canonical paths.
// Mounts on the unauthenticated routegroup so the docs are reachable
// without a session — they're a static description of the public API
// surface and contain no secrets.
func RegisterAPIDocsRoutes(r gin.IRouter) {
	g := r.Group("/_meta")
	g.GET("/openapi.yaml", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", openAPIYAML)
	})
	g.GET("/openapi.json", func(c *gin.Context) {
		var doc any
		if err := yaml.Unmarshal(openAPIYAML, &doc); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "spec_parse_failed",
				"detail": err.Error(),
			})
			return
		}
		// Recursively convert map[interface{}]interface{} → map[string]any
		// so the standard encoding/json marshaller (Gin uses it via
		// c.JSON) doesn't bail on YAML's interface{}-keyed maps.
		c.JSON(http.StatusOK, normalizeYAMLForJSON(doc))
	})
}

// normalizeYAMLForJSON walks the parsed-YAML tree and converts
// map[interface{}]interface{} (the legacy yaml.v2 shape) into
// map[string]any so encoding/json can serialize it. yaml.v3 (which
// we use) returns map[string]any natively for plain maps, so this
// is a no-op on yaml.v3 output — kept defensively in case the spec
// grows non-string keys.
func normalizeYAMLForJSON(v any) any {
	switch m := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[toString(k)] = normalizeYAMLForJSON(val)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = normalizeYAMLForJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(m))
		for i, val := range m {
			out[i] = normalizeYAMLForJSON(val)
		}
		return out
	default:
		return v
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
