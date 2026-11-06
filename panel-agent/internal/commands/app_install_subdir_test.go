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
		for _, sub := range []string{"..", "../x", "../../../../etc", "a/../../b", "/../.."} {
			if p, err := fn(subdirTestDocroot, sub); err == nil {
				t.Errorf("%s: subdirectory %q accepted as %q", name, sub, p)
			}
		}
		for sub, want := range map[string]string{
			"":       subdirTestDocroot,
			"blog":   subdirTestDocroot + "/blog",
			"/blog/": subdirTestDocroot + "/blog",
			"a/b":    subdirTestDocroot + "/a/b",
		} {
			if p, err := fn(subdirTestDocroot, sub); err != nil || p != want {
				t.Errorf("%s: subdirectory %q gave %q, %v; want %q", name, sub, p, err, want)
			}
		}
	}
}

// Every install, delete and clone handler that takes a subdirectory refuses
// one that leaves the docroot, before it runs anything.
func TestAppHandlers_RefuseASubdirectoryOutsideTheDocroot(t *testing.T) {
	install := map[string]any{
		"os_user": "u", "docroot": subdirTestDocroot, "subdirectory": "../../../../etc",
		"db_name": "db", "db_user": "dbu", "db_password": "dbpass123",
		"site_title": "Site", "admin_user": "Admin1", "admin_pass": "Passw0rdPassw0rd",
		"admin_email": "a@example.com", "company_name": "Co", "admin_name": "Admin",
	}
	remove := map[string]any{"os_user": "u", "docroot": subdirTestDocroot, "subdirectory": "../../../../etc"}
	clone := map[string]any{
		"os_user": "u", "src_docroot": subdirTestDocroot, "dst_docroot": subdirTestDocroot,
		"src_db_name": "db", "dst_db_name": "db2", "dst_db_user": "dbu", "dst_db_password": "dbpass123",
		"dst_db_host": "localhost", "src_site_url": "https://example.com", "dst_site_url": "https://example.net",
		"dst_subdirectory": "../../../../etc",
	}
	type handler func(context.Context, json.RawMessage) (any, error)
	for name, tc := range map[string]struct {
		h      handler
		params map[string]any
	}{
		"flarum.install":     {flarumInstallHandler, install},
		"flarum.delete":      {flarumDeleteHandler, remove},
		"joomla.install":     {joomlaInstallHandler, install},
		"joomla.delete":      {joomlaDeleteHandler, remove},
		"mediawiki.install":  {mediawikiInstallHandler, install},
		"mediawiki.delete":   {mediawikiDeleteHandler, remove},
		"phpbb.install":      {phpbbInstallHandler, install},
		"phpbb.delete":       {phpbbDeleteHandler, remove},
		"opencart.install":   {opencartInstallHandler, install},
		"opencart.delete":    {opencartDeleteHandler, remove},
		"prestashop.install": {prestashopInstallHandler, install},
		"prestashop.delete":  {prestashopDeleteHandler, remove},
		"itflow.install":     {itflowInstallHandler, install},
		"itflow.delete":      {itflowDeleteHandler, remove},
		"wordpress.clone":    {wordpressCloneHandler, clone},
	} {
		raw, _ := json.Marshal(tc.params)
		_, err := tc.h(context.Background(), raw)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument || !strings.Contains(ae.Message, "escapes docroot") {
			t.Errorf("%s: got %v, want an invalid-argument error saying the subdirectory escapes the docroot", name, err)
		}
	}
}
