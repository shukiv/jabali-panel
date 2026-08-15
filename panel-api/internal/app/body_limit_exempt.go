// JAB-246: routes exempt from the global request-body cap.
package app

// bodyLimitExemptRoutes lists the routes that legitimately accept bodies
// larger than middleware.DefaultBodyLimitBytes. Every entry MUST enforce
// its own byte cap inside the route (http.MaxBytesReader, io.LimitReader,
// or an explicit size check) — the global middleware skips them entirely,
// and nesting a second reader would silently truncate real uploads.
//
// Keys are "METHOD " + the Gin route template (c.FullPath()).
// TestBodyLimitExemptRoutes_ExistInRouteTable pins each entry to the real
// route table so a route rename turns into a red test instead of a
// silently re-capped upload.
var bodyLimitExemptRoutes = map[string]struct{}{
	// Own cap: filesUploadSizeLimit (server_settings.upload_max_size_mb).
	"POST /api/v1/files/upload": {},
	// Own cap: io.LimitReader against upload_max_size_mb per chunk.
	"POST /api/v1/files/upload-chunk": {},
	// Own cap: filesUploadSizeLimit — Monaco editor saves whole files.
	"POST /api/v1/files/write": {},
	// Own cap: http.MaxBytesReader against upload_max_size_mb.
	"POST /api/v1/databases/:id/restore": {},
	// Own cap: http.MaxBytesReader against upload_max_size_mb.
	"POST /api/v1/admin/migrations/:id/tarball": {},
	// Own cap: http.MaxBytesReader against MaxLogoBytes.
	"POST /api/v1/admin/settings/branding/logo/:variant": {},
	// Own cap: http.MaxBytesReader against speedtestMaxBytes; the handler
	// drains and discards — buffering here would defeat the measurement.
	"POST /api/v1/admin/speedtest/upload": {},
}
