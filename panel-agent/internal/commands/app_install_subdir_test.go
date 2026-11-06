package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const subdirTestDocroot = "/home/u/domains/example.com/public_html"

// Subdirectories that leave the docroot, and ones that name the docroot
// itself. A deleter would remove the whole docroot as the app's folder for
// the second kind.
var (
	subdirsOutside  = []string{"..", "../x", "../../../../etc", "a/../../b", "/../.."}
	subdirsTheRoot  = []string{".", "/", "a/..", "/./", "./"}
	subdirsAccepted = map[string]string{
		"blog":   subdirTestDocroot + "/blog",
		"/blog/": subdirTestDocroot + "/blog",
		"a/b":    subdirTestDocroot + "/a/b",
	}
)

func TestAppInstallPath_RefusesTheDocrootItself(t *testing.T) {
	for _, sub := range subdirsTheRoot {
		if p, err := appInstallPath(subdirTestDocroot, sub); err == nil {
			t.Errorf("subdirectory %q accepted as %q", sub, p)
		}
	}
	if p, err := appInstallPath(subdirTestDocroot, ""); err != nil || p != subdirTestDocroot {
		t.Errorf("empty subdirectory gave %q, %v; want the docroot", p, err)
	}
}

// The installers' path helpers keep a subdirectory inside the docroot, as
// appInstallPath does for Drupal and WordPress.
func TestInstallPathHelpers_KeepTheSubdirectoryInTheDocroot(t *testing.T) {
	helpers := map[string]func(string, string) (string, error){
		"flarum":     computeFlarumInstallPath,
		"joomla":     computeJoomlaInstallPath,
		"mediawiki":  computeMediaWikiInstallPath,
		"phpbb":      computePhpbbInstallPath,
		"opencart":   computeOpenCartInstallPath,
		"prestashop": computePrestaShopInstallPath,
		"itflow":     computeITFlowInstallPath,
	}
	for name, fn := range helpers {
		for _, sub := range append(append([]string{}, subdirsOutside...), subdirsTheRoot...) {
			if p, err := fn(subdirTestDocroot, sub); err == nil {
				t.Errorf("%s: subdirectory %q accepted as %q", name, sub, p)
			}
		}
		if p, err := fn(subdirTestDocroot, ""); err != nil || p != subdirTestDocroot {
			t.Errorf("%s: empty subdirectory gave %q, %v; want the docroot", name, p, err)
		}
		for sub, want := range subdirsAccepted {
			if p, err := fn(subdirTestDocroot, sub); err != nil || p != want {
				t.Errorf("%s: subdirectory %q gave %q, %v; want %q", name, sub, p, err, want)
			}
		}
	}
}

// Every install, delete and clone handler that takes a subdirectory refuses
// one that leaves the docroot, and every deleter refuses one that names the
// docroot itself, before it runs anything.
func TestAppHandlers_RefuseASubdirectoryOutsideTheDocroot(t *testing.T) {
	install := func(sub string) map[string]any {
		return map[string]any{
			"os_user": "u", "docroot": subdirTestDocroot, "subdirectory": sub,
			"db_name": "db", "db_user": "dbu", "db_password": "dbpass123",
			"site_title": "Site", "admin_user": "Admin1", "admin_pass": "Passw0rdPassw0rd",
			"admin_email": "a@example.com", "company_name": "Co", "admin_name": "Admin",
		}
	}
	remove := func(sub string) map[string]any {
		return map[string]any{"os_user": "u", "docroot": subdirTestDocroot, "subdirectory": sub}
	}
	clone := func(sub string) map[string]any {
		return map[string]any{
			"os_user": "u", "src_docroot": subdirTestDocroot, "dst_docroot": subdirTestDocroot,
			"src_db_name": "db", "dst_db_name": "db2", "dst_db_user": "dbu", "dst_db_password": "dbpass123",
			"dst_db_host": "localhost", "src_site_url": "https://example.com", "dst_site_url": "https://example.net",
			"dst_subdirectory": sub,
		}
	}
	type handler func(context.Context, json.RawMessage) (any, error)
	type tcase struct {
		h       handler
		params  func(string) map[string]any
		deleter bool
	}
	for name, tc := range map[string]tcase{
		"flarum.install":     {flarumInstallHandler, install, false},
		"flarum.delete":      {flarumDeleteHandler, remove, true},
		"joomla.install":     {joomlaInstallHandler, install, false},
		"joomla.delete":      {joomlaDeleteHandler, remove, true},
		"mediawiki.install":  {mediawikiInstallHandler, install, false},
		"mediawiki.delete":   {mediawikiDeleteHandler, remove, true},
		"phpbb.install":      {phpbbInstallHandler, install, false},
		"phpbb.delete":       {phpbbDeleteHandler, remove, true},
		"opencart.install":   {opencartInstallHandler, install, false},
		"opencart.delete":    {opencartDeleteHandler, remove, true},
		"prestashop.install": {prestashopInstallHandler, install, false},
		"prestashop.delete":  {prestashopDeleteHandler, remove, true},
		"itflow.install":     {itflowInstallHandler, install, false},
		"itflow.delete":      {itflowDeleteHandler, remove, true},
		"wordpress.delete":   {wordpressDeleteHandler, remove, true},
		"moodle.delete":      {moodleDeleteHandler, remove, true},
		"wordpress.clone":    {wordpressCloneHandler, clone, false},
	} {
		subs := []string{"../../../../etc"}
		if tc.deleter {
			subs = append(subs, ".", "a/..")
			if name != "wordpress.delete" {
				// wordpress.delete treats "/" as a root install: it removes
				// only WordPress's own files, never the docroot.
				subs = append(subs, "/")
			}
		}
		for _, sub := range subs {
			raw, _ := json.Marshal(tc.params(sub))
			_, err := tc.h(context.Background(), raw)
			var ae *agentwire.AgentError
			if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument || !strings.Contains(ae.Message, "docroot") {
				t.Errorf("%s with subdirectory %q: got %v, want an invalid-argument error about the docroot", name, sub, err)
			}
		}
	}
}
