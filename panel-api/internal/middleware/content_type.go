package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// RequireContentType rejects POST/PATCH/PUT requests with a missing or
// unrecognised Content-Type. Accepted types are application/json,
// multipart/form-data (small file uploads), and application/octet-stream
// (the file manager's chunked-upload path streams raw binary chunks — every
// file over 100 MB, GH #211). GET, DELETE, and other body-less methods are
// passed through unchanged.
func RequireContentType() gin.HandlerFunc {
	return func(c *gin.Context) {
		m := c.Request.Method
		if m != http.MethodPost && m != http.MethodPatch && m != http.MethodPut {
			c.Next()
			return
		}
		// Body-less POSTs (e.g. action triggers where version is a URL param)
		// carry no payload and therefore need no Content-Type.
		if c.Request.ContentLength == 0 || c.Request.Body == nil || c.Request.Body == http.NoBody {
			c.Next()
			return
		}
		// Outlook autodiscover (GH #1039) POSTs a fixed text/xml request body to
		// a well-known path whose Content-Type we don't control. It's a public
		// discovery endpoint that parses + validates its own XML body and escapes
		// its output, so exempt it from the JSON-or-upload guard rather than
		// widen the accepted set for every endpoint.
		if strings.EqualFold(c.Request.URL.Path, "/autodiscover/autodiscover.xml") {
			c.Next()
			return
		}

		ct := c.GetHeader("Content-Type")
		if !strings.HasPrefix(ct, "application/json") &&
			!strings.HasPrefix(ct, "multipart/form-data") &&
			!strings.HasPrefix(ct, "application/octet-stream") {
			c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, gin.H{
				"error": "unsupported_content_type",
			})
			return
		}
		c.Next()
	}
}
