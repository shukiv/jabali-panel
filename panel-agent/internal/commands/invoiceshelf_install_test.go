package commands

import (
	"strings"
	"testing"
)

// GH #631: the flattened-Laravel nginx snippet must deny every sensitive
// app dir/file that now sits in the webroot, and beat the vhost's
// `~ \.php$` with = / ^~ precedence. A gap here means /vendor/*.php is
// executable or /.env is readable.
func TestInvoiceShelfNginxConf_DeniesSensitivePaths(t *testing.T) {
	conf := invoiceShelfNginxConf("alice", "invoices")
	for _, must := range []string{
		"location = /invoices/.env { deny all; return 404; }",
		"location = /invoices/artisan { deny all; return 404; }",
		"location ^~ /invoices/vendor/ { deny all; return 404; }",
		"location ^~ /invoices/storage/ { deny all; return 404; }",
		"location ^~ /invoices/config/ { deny all; return 404; }",
		"location ^~ /invoices/database/ { deny all; return 404; }",
		"try_files $uri $uri/ /invoices/index.php?$query_string;",
	} {
		if !strings.Contains(conf, must) {
			t.Errorf("nginx conf missing %q\n%s", must, conf)
		}
	}
	// Docroot install: deny blocks present, but no subdir front controller.
	root := invoiceShelfNginxConf("alice", "")
	if !strings.Contains(root, "location ^~ /vendor/ { deny all; return 404; }") {
		t.Error("docroot conf must still deny /vendor/")
	}
	if strings.Contains(root, "try_files") {
		t.Error("docroot install rides the generic vhost front controller — no try_files")
	}
}

// envQuoted must strip characters that would break a .env line.
func TestEnvQuoted(t *testing.T) {
	if got := envQuoted("pa\"ss\nword"); strings.ContainsAny(got[1:len(got)-1], "\"\n\r") {
		t.Errorf("quotes/newlines survived: %q", got)
	}
}

// Security review: removeInvoiceShelfNginx must reject a traversal subdir
// before it reaches the snippet FILENAME (a "../.." would let os.Remove,
// running as root, escape the domain dir and delete an arbitrary .conf).
func TestRemoveInvoiceShelfNginx_RejectsTraversalSubdir(t *testing.T) {
	for _, bad := range []string{"../../etc/nginx/evil", "..", "a/../../b", "foo/../../bar"} {
		if err := removeInvoiceShelfNginx(nil, "example.com", bad); err == nil {
			t.Errorf("removeInvoiceShelfNginx accepted traversal subdir %q", bad)
		}
	}
}
