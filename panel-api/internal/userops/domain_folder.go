package userops

import (
	"context"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// domainFolder returns the domain's own folder, /home/<user>/domains/<name>,
// when docRoot is that folder or inside it, else "". A change of owner
// moves that whole folder (GH #2541), not only the docroot, so what
// sits next to the docroot goes with it: a Laravel-style site served from
// public_html/public keeps its app code, vendor/ and .env.
func domainFolder(username, name, docRoot string) string {
	if username == "" || name == "" {
		return ""
	}
	dir := "/home/" + username + "/domains/" + name
	if docRoot == dir || strings.HasPrefix(docRoot, dir+"/") {
		return dir
	}
	return ""
}

// siteInsideTree returns another of userID's domains whose docroot is tree or
// inside it, or nil. Moving tree would carry that site's files away from its
// docroot, so a move refuses on one. Only an admin can set a docroot like that.
func siteInsideTree(ctx context.Context, domains repository.DomainRepository, userID, domainID, tree string) (*models.Domain, error) {
	owned, _, err := domains.ListByUserID(ctx, userID, repository.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range owned {
		o := &owned[i]
		if o.ID == domainID || o.DocRoot == "" {
			continue
		}
		if o.DocRoot == tree || strings.HasPrefix(o.DocRoot, tree+"/") {
			return o, nil
		}
	}
	return nil, nil
}
