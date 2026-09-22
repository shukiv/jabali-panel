package api

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// parseListOptions pulls the standard list query-string params
// (?page=, ?page_size=, ?q=, ?sort=, ?order=) off a gin.Context and
// returns a repository.ListOptions with Offset/Limit already computed.
//
// Defensive defaults: page<1 and pageSize out of range both clamp to the
// caller-supplied defaults; search/sort/order are passed through verbatim
// and validated by the repo's allowlist (applyListOptions).
func parseListOptions(c *gin.Context, defaultPageSize, maxPageSize int) (page int, pageSize int, opts repository.ListOptions) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", strconv.Itoa(defaultPageSize)))
	// A missing/invalid (<1) size falls back to the default, but an
	// over-max request clamps DOWN to the max — it must not silently reset
	// to the (small) default. Selectors legitimately over-request to fill a
	// dropdown (e.g. page_size=500 vs a max of 200); resetting them to the
	// default page (20) is why such lists showed only the first page (GH #1796).
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	opts = repository.ListOptions{
		Offset: (page - 1) * pageSize,
		Limit:  pageSize,
		Search: c.Query("q"),
		Sort:   c.Query("sort"),
		Order:  c.Query("order"),
	}
	return page, pageSize, opts
}
