package appsecops

import (
	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

// nextcloud.go — Discussion #1032. A Nextcloud install gets CRS 911100 dropped
// on its WebDAV endpoints.
//
// On Jabali, CRS 911100 allows only GET, HEAD, POST and OPTIONS. Nextcloud's
// Files app, its desktop and mobile sync clients, and CalDAV/CardDAV all speak
// WebDAV: PROPFIND, PROPPATCH, REPORT, MKCOL, PUT, MOVE, COPY, LOCK and DELETE.
// Every one of them is a 403 under that rule, so the Files app cannot even
// list a folder. Nextcloud authenticates and authorises each DAV request
// itself (a session, an app password, or a public-share token), so these
// methods are safe to let through to it.
//
// The exemption is narrow: rule 911100 only, on the install's own host, on the
// DAV endpoints only. Every other CRS rule still evaluates those requests, and
// the rest of the site keeps the method policy.

// AppTypeNextcloud is the application_installs.app_type of a Nextcloud install.
const AppTypeNextcloud = "nextcloud"

const (
	nextcloudRuleID     = "911100"
	nextcloudNotePrefix = "Nextcloud WebDAV methods (CRS 911100), managed for Nextcloud install "
	nextcloudNoteSuffix = " (Discussion #1032)"
)

// nextcloudDAVPaths are Nextcloud's WebDAV endpoints: the authenticated DAV
// root (files, calendars, contacts), the legacy files endpoint older clients
// use, and the public-share equivalents of both.
var nextcloudDAVPaths = []string{
	"/remote.php/dav/",
	"/remote.php/webdav/",
	"/public.php/dav/",
	"/public.php/webdav/",
}

var nextcloudApp = managedApp{
	appType:    AppTypeNextcloud,
	ruleID:     nextcloudRuleID,
	notePrefix: nextcloudNotePrefix,
	noteSuffix: nextcloudNoteSuffix,
	exclusions: NextcloudExclusions,
}

// NextcloudExclusions returns the exclusions a Nextcloud install needs: CRS
// 911100 dropped for each WebDAV endpoint, on the host the install is served
// from. The error is ValidateExclusion's; an install whose exclusions fail it
// gets none.
func NextcloudExclusions(installID, domainName string, useWWW bool, subdirectory string) ([]appseccfg.Exclusion, error) {
	host := installHost(domainName, useWWW)
	out := make([]appseccfg.Exclusion, 0, len(nextcloudDAVPaths))
	for _, p := range nextcloudDAVPaths {
		e := appseccfg.Exclusion{
			Host:      host,
			URIPrefix: installPathPrefix(subdirectory, p),
			RuleID:    nextcloudRuleID,
			Note:      nextcloudNotePrefix + installID + nextcloudNoteSuffix,
		}
		if err := appseccfg.ValidateExclusion(e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
