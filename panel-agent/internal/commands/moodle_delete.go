package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type moodleDeleteReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type moodleDeleteResp struct {
	Status string `json:"status"`
}

// moodleTopLevel enumerates the entries Moodle's tarball lays down at the
// install root (after --strip-components=1), plus config.php from the CLI
// installer. For a docroot install we enumerate instead of `rm -rf` so any
// user-uploaded sibling files survive; for a subdir install the whole subdir
// is removed. Generated from Moodle 4.5; bump if a release adds a top entry.
var moodleTopLevel = []string{
	"admin", "auth", "availability", "backup", "badges", "blocks", "blog",
	"brokenfile.php", "cache", "calendar", "cohort", "comment", "competency",
	"completion", "config-dist.php", "config.php", "contentbank", "course",
	"customfield", "dataformat", "draftfile.php", "enrol", "error", "favourites",
	"files", "filter", "githash.php", "grade", "group", "h5p", "help.php",
	"help_ajax.php", "index.php", "install", "install.php", "iplookup", "lang",
	"lib", "local", "login", "media", "message", "mnet", "mod", "my", "notes",
	"npm-shrinkwrap.json", "package.json", "payment", "pix", "plagiarism",
	"portfolio", "privacy", "question", "rating", "report", "reportbuilder",
	"repository", "rss", "search", "tag", "tag.php", "theme", "TRADEMARK.txt",
	"user", "userpix", "version.php", "webservice", "COPYING.txt", "README.txt",
	"composer.json", "composer.lock", "Gruntfile.js", "phpunit.xml.dist",
	".grunt", "vendor", "node_modules",
}

func moodleDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req moodleDeleteReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if req.OSUser == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user is required"}
	}
	if req.Docroot == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "docroot is required"}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid docroot: %v", err)}
	}
	if err := validateMoodleSubdir(req.Docroot, req.Subdirectory); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath := computeMoodleInstallPath(req.Docroot, req.Subdirectory)
	dataRoot := computeMoodleDataRoot(req.Docroot, req.Subdirectory)

	if req.Subdirectory != "" {
		_ = buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", installPath).Run()
	} else {
		for _, name := range moodleTopLevel {
			_ = buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", filepath.Join(installPath, name)).Run()
		}
	}

	// Remove the moodledata tree (outside the docroot) so deleting an app
	// doesn't orphan its uploaded content + cache.
	_ = buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", dataRoot).Run()

	return moodleDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("moodle", moodleDeleteHandler)
}
