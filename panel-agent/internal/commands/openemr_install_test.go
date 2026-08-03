package commands

import (
	"strings"
	"testing"
)

// GH #631: the OpenEMR nginx snippet must deny the PHI + setup surfaces
// (patient documents, the auto-installer, sqlconf.php) with = / ^~
// precedence that beats the vhost's `~ \.php$`. A gap here leaks patient
// records or lets the installer be re-triggered over the web.
func TestOpenEMRNginxConf_DeniesPHIAndSetup(t *testing.T) {
	conf := openemrNginxConf("alice", "openemr")
	for _, must := range []string{
		"location ^~ /openemr/sites/default/documents/ { deny all; return 404; }",
		"location ^~ /openemr/sql/ { deny all; return 404; }",
		"location = /openemr/contrib/util/installScripts/InstallerAuto.php { deny all; return 404; }",
		"location = /openemr/sites/default/sqlconf.php { deny all; return 404; }",
		"try_files $uri $uri/ /openemr/index.php?$query_string;",
	} {
		if !strings.Contains(conf, must) {
			t.Errorf("nginx conf missing %q\n%s", must, conf)
		}
	}
	root := openemrNginxConf("alice", "")
	if !strings.Contains(root, "location ^~ /sites/default/documents/ { deny all; return 404; }") {
		t.Error("docroot conf must still deny PHI documents")
	}
	if strings.Contains(root, "try_files") {
		t.Error("docroot install rides the generic vhost front controller")
	}
}

// The auto-installer accepts key=value argv; an admin password with '='
// would be truncated, so it must be rejected (also newlines).
func TestOpenEMRUsernameAndPasswordGuards(t *testing.T) {
	if openemrUsernamePattern.MatchString("bad user") {
		t.Error("username pattern must reject spaces")
	}
	if !openemrUsernamePattern.MatchString("admin.smith_1") {
		t.Error("username pattern must accept [A-Za-z0-9_.-]")
	}
}

// Same nginx-remove containment as the other apps: a traversal subdir must
// be refused before it reaches the snippet filename.
func TestRemoveOpenEMRNginx_RejectsTraversalSubdir(t *testing.T) {
	for _, bad := range []string{"../../etc/nginx/x", "..", "a/../../b"} {
		if err := removeOpenEMRNginx(nil, "example.com", bad); err == nil {
			t.Errorf("removeOpenEMRNginx accepted traversal subdir %q", bad)
		}
	}
}
