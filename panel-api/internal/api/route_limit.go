package api

import "github.com/gin-gonic/gin"

// limitedRoute is the handler chain for a route behind an optional per-route
// limit: the limit first when it is set, the handler alone when it is nil. A
// route carries the limit only when it is wired, which the app's route tests
// can see.
func limitedRoute(limit, h gin.HandlerFunc) []gin.HandlerFunc {
	if limit == nil {
		return []gin.HandlerFunc{h}
	}
	return []gin.HandlerFunc{limit, h}
}
