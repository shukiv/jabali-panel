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
	conf := invoiceShelfNginxConf("alice", "invoices", "/home/alice/domains/x.com/public_html/invoices")
	for _, must := range []string{
		"location = /invoices/.env { deny all; return 404; }",
		"location = /invoices/artisan { deny all; return 404; }",
		"location ^~ /invoices/vendor/ { deny all; return 404; }",
		"location ^~ /invoices/config/ { deny all; return 404; }",
		"location ^~ /invoices/database/ { deny all; return 404; }",
		"try_files $uri $uri/ /invoices/index.php?$query_string;",
	} {
		if !strings.Contains(conf, must) {
			t.Errorf("nginx conf missing %q\n%s", must, conf)
		}
	}
	// Docroot install: deny blocks present, but no subdir front controller.
	root := invoiceShelfNginxConf("alice", "", "/home/alice/domains/x.com/public_html")
	if !strings.Contains(root, "location ^~ /vendor/ { deny all; return 404; }") {
		t.Error("docroot conf must still deny /vendor/")
	}
	if strings.Contains(root, "try_files") {
		t.Error("docroot install rides the generic vhost front controller — no try_files")
	}
}

// GH #1042: uploads live on Laravel's public disk (storage/app/public) and
// are linked as /storage/<path>. The old snippet blanket-denied /storage/,
// so every upload 404'd after a successful save. The alias must serve
// exactly the public disk — and ONLY it: a blanket deny AND the alias for
// the same ^~ prefix would be an nginx duplicate-location error, and the
// alias remap is itself what keeps storage/framework + storage/logs
// unreachable.
func TestInvoiceShelfNginxConf_StorageAliasServesUploads(t *testing.T) {
	for _, tc := range []struct{ subdir, prefix, install string }{
		{"", "", "/home/alice/domains/x.com/public_html"},
		{"invoices", "/invoices", "/home/alice/domains/x.com/public_html/invoices"},
	} {
		conf := invoiceShelfNginxConf("alice", tc.subdir, tc.install)
		if strings.Contains(conf, "location ^~ "+tc.prefix+"/storage/ { deny all") {
			t.Errorf("subdir=%q: /storage/ is still blanket-denied — uploads 404 (the #1042 bug)", tc.subdir)
		}
		if !strings.Contains(conf, "alias "+tc.install+"/storage/app/public/;") {
			t.Errorf("subdir=%q: alias to the public disk missing\n%s", tc.subdir, conf)
		}
		// An uploaded .php must not be served or executed.
		if !strings.Contains(conf, `location ~ \.php$ { deny all; return 404; }`) {
			t.Errorf("subdir=%q: nested php deny missing under the storage alias", tc.subdir)
		}
		// The alias/try_files trap: try_files under alias resolves against
		// root, not the alias target — it must not appear in the block.
		storageBlock := conf[strings.Index(conf, "location ^~ "+tc.prefix+"/storage/"):]
		storageBlock = storageBlock[:strings.Index(storageBlock, "}\n")+1]
		if strings.Contains(storageBlock, "try_files") {
			t.Errorf("subdir=%q: try_files inside the alias block — the classic nginx alias trap", tc.subdir)
		}
	}
}

// GH #1042: the finalisation script must scrub the seeder's placeholder
// identity and resolve the requested currency against the app's own table.
func TestInvoiceShelfFinaliseTinker(t *testing.T) {
	tk := invoiceshelfFinaliseTinker("admin@example.com")
	for _, must := range []string{
		`$u->name = 'Administrator';`,      // "Jane Doe" must not survive
		`$u->email = 'admin@example.com';`, // login identity
		`getenv('IS_ADMIN_PASS')`,          // secret via env, never argv
		`Currency::where('code', getenv('IS_CURRENCY'))`,
		`CompanySetting::setSettings(['currency' => $cur->id], $c->id)`,
		`if($c && $cur)`, // unknown code keeps the seed, install survives
		// GH #1042 follow-up: country lands on the company address row,
		// exactly where the skipped wizard's company-info step puts it.
		`Country::where('code', getenv('IS_COUNTRY'))`,
		`$a = $c->address()->firstOrNew([]);`,
		`$a->country_id = $ct->id;`,
		`$c->address()->save($a);`,
		`if($c && $ct)`, // unknown code leaves no address, install survives
		`Setting::setSetting('profile_complete','COMPLETED')`,
	} {
		if !strings.Contains(tk, must) {
			t.Errorf("tinker missing %q\n%s", must, tk)
		}
	}
}

// envQuoted must strip characters that would break a .env line.
func TestEnvQuoted(t *testing.T) {
	if got := envQuoted("pa\"ss\nword"); strings.ContainsAny(got[1:len(got)-1], "\"\n\r") {
		t.Errorf("quotes/newlines survived: %q", got)
	}
}

// GH #1042 follow-up: psysh's config dir must live under /tmp, NOT
// $HOME — jabali tenant homes are root-owned 0751, so psysh's default
// $HOME/.config is unwritable and tinker exits 1 before running.
func TestPsyshConfigHome(t *testing.T) {
	got := psyshConfigHome("alice")
	if !strings.HasPrefix(got, "/tmp/") {
		t.Errorf("psysh home %q must be /tmp-scoped (tenant homes are root-owned)", got)
	}
	if !strings.HasSuffix(got, "alice") {
		t.Errorf("psysh home %q must be per-user (history file lands there)", got)
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
