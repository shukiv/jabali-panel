package middleware

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// DefaultBodyLimitBytes caps request bodies on every JSON route (JAB-246).
// The panel-facing nginx vhost allows bodies up to upload_max_size_mb
// (10 GiB by default) because a handful of upload routes genuinely need
// that; every other handler binds JSON straight into memory, so without
// a pre-decode cap an authenticated tenant can force multi-gigabyte
// allocations and OOM the panel. 1 MiB is far above any legitimate JSON
// body the API accepts (the largest are catalog compose documents and
// DNS zone payloads, all well under 100 KiB).
const DefaultBodyLimitBytes int64 = 1 << 20

// BodyLimit rejects request bodies larger than limit with a deterministic
// 413 before any handler reads them. Routes in exempt (keyed as
// "METHOD /full/route/:template") manage their own byte caps — nesting a
// second reader here would silently truncate legitimate uploads.
//
// Chunked bodies carry no Content-Length, so the header check alone is
// bypassable; the body is buffered through a LimitReader to make the cap
// hold regardless of transfer encoding. The buffer is bounded by limit,
// which is the same order of allocation the JSON decoder would perform —
// the bound itself is the fix. Request bodies are never decompressed
// anywhere in panel-api, so the cap applies to raw bytes and cannot be
// bypassed with a compressed payload.
func BodyLimit(limit int64, exempt map[string]struct{}) gin.HandlerFunc {
	return func(c *gin.Context) {
		m := c.Request.Method
		// Body-less methods: handlers never read these bodies, and buffering
		// them would let a GET with a giant body waste work before the 404.
		if m != http.MethodPost && m != http.MethodPatch && m != http.MethodPut {
			c.Next()
			return
		}
		if c.Request.Body == nil || c.Request.Body == http.NoBody {
			c.Next()
			return
		}
		if _, ok := exempt[m+" "+c.FullPath()]; ok {
			c.Next()
			return
		}
		if c.Request.ContentLength > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "request_body_too_large",
			})
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "invalid_request",
			})
			return
		}
		if int64(len(body)) > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "request_body_too_large",
			})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Next()
	}
}
