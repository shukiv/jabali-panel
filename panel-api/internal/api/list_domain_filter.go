// list_domain_filter.go — the optional ?domain_id= filter on the account-wide
// mail lists (/mail/forwarders, /mail/forwarders/domain-scoped, /mail/shares).
// A mail domain's page sends it so the page lists that domain's rows only,
// not every mail domain of the account (GH #1997).

package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// listDomainFilter resolves ?domain_id=. Without the parameter it returns
// (nil, true) and the list stays account-wide. Otherwise it answers as
// /domains/:id/mailboxes does: a malformed id is 400, an unknown domain 404,
// and another account's domain 403 (an admin may name any domain). When it
// returns false, the response has been written.
func listDomainFilter(c *gin.Context, domains repository.DomainRepository, claims *auth.AccessClaims) (*models.Domain, bool) {
	id, set := c.GetQuery("domain_id")
	if !set {
		return nil, true
	}
	if !ids.IsValidULID(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_domain_id"})
		return nil, false
	}
	dom, err := domains.FindByID(c.Request.Context(), id)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, false
	}
	return dom, true
}
