package commands

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---- nginx drop-in (Discussion #1032) ----

func TestNextcloudNginxConf(t *testing.T) {
	const sock = "/run/php/jabali-bob-php8.4/fpm.sock"
	conf := string(snippetWithPHPPins([]byte(nextcloudNginxConf(sock)), "cloud.example.com"))

	for _, want := range []string{
		// The code/config/data trees and the CLI entry points never serve.
		"location ^~ /config/ { deny all; return 404; }",
		"location ^~ /lib/ { deny all; return 404; }",
		"location ^~ /3rdparty/ { deny all; return 404; }",
		"location ^~ /data/ { deny all; return 404; }",
		"location = /occ { deny all; return 404; }",
		"location = /console.php { deny all; return 404; }",
		"location = /.user.ini { deny all; return 404; }",
		// CalDAV/CardDAV discovery.
		"location = /.well-known/carddav { return 301 /remote.php/dav/; }",
		"location = /.well-known/caldav { return 301 /remote.php/dav/; }",
		// Module scripts need a JavaScript type.
		"location ~* \\.mjs$ { types { text/javascript mjs; } try_files $uri =404; }",
		// Front controllers: ^~ prefixes, one fixed script each.
		"location ^~ /index.php/ {",
		"location ^~ /remote.php/ {",
		"location ^~ /public.php/ {",
		"location ^~ /ocs/v2.php/ {",
		"fastcgi_split_path_info ^(/remote\\.php)(/.*)$;",
		"fastcgi_split_path_info ^(/ocs/v1\\.php)(/.*)$;",
		"fastcgi_pass unix:" + sock + ";",
		"client_max_body_size 512M;",
		"fastcgi_param PATH_INFO $fastcgi_path_info;",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("drop-in is missing %q", want)
		}
	}
	// Every front controller carries the domain's PHP pins (GH #1701), so a
	// reused worker never runs it with another domain's PHP values.
	if got := strings.Count(conf, phpPinsInclude("cloud.example.com")); got != len(nextcloudFrontControllers) {
		t.Errorf("pins include appears %d times, want %d", got, len(nextcloudFrontControllers))
	}
	// No regex .php location: those lose to the vhost's own `~ \.php$`, and a
	// regex PATH_INFO split could walk back onto an uploaded file.
	if strings.Contains(conf, `location ~ \.php`) || strings.Contains(conf, `location ~ ^(`) {
		t.Errorf("drop-in has a regex PHP location:\n%s", conf)
	}
}

// ---- which PHP the domain runs ----

func poolFixture(t *testing.T, vhost string, poolConfs ...string) {
	t.Helper()
	root := t.TempDir()
	sites := filepath.Join(root, "sites")
	if err := os.MkdirAll(sites, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sites, "cloud.example.com.conf"), []byte(vhost), 0o644); err != nil {
		t.Fatal(err)
	}
	php := filepath.Join(root, "php")
	for _, c := range poolConfs {
		p := filepath.Join(php, c)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("[x]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldSites, oldPHP := nginxSitesDir, phpEtcRoot
	nginxSitesDir, phpEtcRoot = sites, php
	t.Cleanup(func() { nginxSitesDir, phpEtcRoot = oldSites, oldPHP })
}

func vhostWith(socket string) string {
	return "server {\n    location = /index.php {\n        fastcgi_pass unix:" + socket + ";\n    }\n}\n"
}

func TestDomainFPMPool(t *testing.T) {
	t.Run("default pool takes its version from its config", func(t *testing.T) {
		poolFixture(t, vhostWith("/run/php/jabali-bob/fpm.sock"), "8.4/fpm/pool.d/jabali-bob.conf", "8.3/fpm/pool.d/jabali-alice.conf")
		p, err := domainFPMPool("cloud.example.com", "bob")
		if err != nil || p.Socket != "/run/php/jabali-bob/fpm.sock" || p.PHPVersion != "8.4" {
			t.Fatalf("got %+v, %v", p, err)
		}
	})
	t.Run("per-version pool takes its version from its slug", func(t *testing.T) {
		poolFixture(t, vhostWith("/run/php/jabali-bob-php8.3/fpm.sock"), "8.2/fpm/pool.d/jabali-bob.conf")
		p, err := domainFPMPool("cloud.example.com", "bob")
		if err != nil || p.Socket != "/run/php/jabali-bob-php8.3/fpm.sock" || p.PHPVersion != "8.3" {
			t.Fatalf("got %+v, %v", p, err)
		}
	})
	for name, tc := range map[string]struct {
		vhost string
		confs []string
	}{
		"another account's pool":        {vhostWith("/run/php/jabali-alice/fpm.sock"), []string{"8.4/fpm/pool.d/jabali-alice.conf"}},
		"another account's versioned":   {vhostWith("/run/php/jabali-bobby-php8.4/fpm.sock"), nil},
		"not a jabali socket":           {vhostWith("/run/php/php8.4-fpm.sock"), nil},
		"no PHP on the domain":          {"server {\n    root /x;\n}\n", nil},
		"default pool config missing":   {vhostWith("/run/php/jabali-bob/fpm.sock"), nil},
		"default pool config ambiguous": {vhostWith("/run/php/jabali-bob/fpm.sock"), []string{"8.3/fpm/pool.d/jabali-bob.conf", "8.4/fpm/pool.d/jabali-bob.conf"}},
	} {
		t.Run(name, func(t *testing.T) {
			poolFixture(t, tc.vhost, tc.confs...)
			if p, err := domainFPMPool("cloud.example.com", "bob"); err == nil {
				t.Fatalf("want an error, got %+v", p)
			}
		})
	}
}

func TestNextcloudSupportsPHP(t *testing.T) {
	for v, want := range map[string]bool{
		"8.2": false, "8.3": true, "8.4": true, "8.5": true, "8.6": false,
		"9.0": false, "7.4": false, "": false, "8": false, "8.x": false,
	} {
		if got := nextcloudSupportsPHP(v); got != want {
			t.Errorf("nextcloudSupportsPHP(%q) = %v, want %v", v, got, want)
		}
	}
}

// ---- the install run ----

func validNextcloudReq() nextcloudInstallReq {
	return nextcloudInstallReq{
		OSUser: "bob", Docroot: "/home/bob/domains/cloud.example.com/public_html",
		SiteURL: "https://cloud.example.com", DBName: "bob_nc", DBUser: "bob_nc",
		DBPassword: "db-S3cret-value", AdminUser: "zqxwvu", AdminPass: "admin-S3cret-value",
		AdminEmail: "admin@example.com",
	}
}

// The secrets reach the installer on stdin only. A transient unit's argv is
// logged to the journal and, like its environment, readable by every local
// user through `systemctl show` while it runs.
func TestNextcloudInstallCmd_SecretsOnlyOnStdin(t *testing.T) {
	req := validNextcloudReq()
	req.DBHost = "localhost"
	cmd, err := nextcloudInstallCmd(context.Background(), req, "/usr/bin/php8.4", "/var/lib/jabali/staging/x/nextcloud-install.php",
		req.Docroot, "/home/bob/domains/cloud.example.com/nextcloud-data")
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(cmd.Args, " ")
	for _, secret := range []string{req.DBPassword, req.AdminPass} {
		if strings.Contains(argv, secret) || strings.Contains(strings.Join(cmd.Env, " "), secret) {
			t.Fatalf("a secret is on the command line or in the environment: %s", argv)
		}
	}
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "--setenv=") && !strings.HasPrefix(a, "--setenv=PATH=") {
			t.Fatalf("nothing but PATH may travel in the unit's environment: %s", a)
		}
	}
	if !strings.Contains(argv, "--uid=bob") || !strings.Contains(argv, "/usr/bin/php8.4") {
		t.Fatalf("not run as the tenant with the pool's PHP: %s", argv)
	}
	if cmd.Stdin == nil {
		t.Fatal("no stdin")
	}
	raw, _ := io.ReadAll(cmd.Stdin)
	var in nextcloudShimInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	want := nextcloudShimInput{
		InstallPath: req.Docroot, DBName: "bob_nc", DBHost: "localhost", DBUser: "bob_nc",
		DBPass: req.DBPassword, AdminUser: "zqxwvu", AdminPass: req.AdminPass,
		AdminEmail: "admin@example.com", DataDir: "/home/bob/domains/cloud.example.com/nextcloud-data",
	}
	if in != want {
		t.Fatalf("stdin = %+v, want %+v", in, want)
	}
}

func TestValidateNextcloudReq(t *testing.T) {
	ok := validNextcloudReq()
	if err := validateNextcloudReq(&ok); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if ok.DBHost != "localhost" {
		t.Fatalf("db_host default = %q, want localhost", ok.DBHost)
	}
	for name, mut := range map[string]func(r *nextcloudInstallReq){
		"bad os user":              func(r *nextcloudInstallReq) { r.OSUser = "Bob;" },
		"docroot outside domains":  func(r *nextcloudInstallReq) { r.Docroot = "/home/alice/domains/x/public_html" },
		"docroot traversal":        func(r *nextcloudInstallReq) { r.Docroot = "/home/bob/domains/../../alice/x" },
		"db name injection":        func(r *nextcloudInstallReq) { r.DBName = "bob_nc;drop" },
		"db user missing":          func(r *nextcloudInstallReq) { r.DBUser = "" },
		"db password newline":      func(r *nextcloudInstallReq) { r.DBPassword = "a\nb" },
		"db host bad":              func(r *nextcloudInstallReq) { r.DBHost = "-h evil" },
		"admin user space":         func(r *nextcloudInstallReq) { r.AdminUser = "ad min" },
		"admin user option-like":   func(r *nextcloudInstallReq) { r.AdminUser = "-x" },
		"admin pass missing":       func(r *nextcloudInstallReq) { r.AdminPass = "" },
		"admin email display name": func(r *nextcloudInstallReq) { r.AdminEmail = "Admin <a@example.com>" },
		"admin email garbage":      func(r *nextcloudInstallReq) { r.AdminEmail = "not-an-email" },
	} {
		t.Run(name, func(t *testing.T) {
			r := validNextcloudReq()
			mut(&r)
			if err := validateNextcloudReq(&r); err == nil {
				t.Fatalf("accepted %+v", r)
			}
		})
	}
}

func TestNextcloudDataDir(t *testing.T) {
	got, err := nextcloudDataDir("bob", "/home/bob/domains/cloud.example.com/public_html")
	if err != nil || got != "/home/bob/domains/cloud.example.com/nextcloud-data" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := nextcloudDataDir("bob", "/home/bob"); err == nil {
		t.Fatalf("a data dir outside the home was allowed: %q", got)
	}
}

// The shim turns its stdin into occ's argv and runs console.php, exactly as
// occ would. A stub console.php records what it was given.
func TestNextcloudInstallShim_BuildsOccArgv(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php CLI not installed")
	}
	root := t.TempDir()
	stub := `<?php file_put_contents(__DIR__ . "/argv.json", json_encode($_SERVER["argv"])); echo getcwd();`
	if err := os.WriteFile(filepath.Join(root, "console.php"), []byte(stub), 0o644); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(t.TempDir(), "shim.php")
	if err := os.WriteFile(shim, []byte(nextcloudInstallShim), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(nextcloudShimInput{
		InstallPath: root, DBName: "bob_nc", DBHost: "localhost", DBUser: "bob_nc", DBPass: "p'a\"ss word",
		AdminUser: "zqxwvu", AdminPass: "adm $(id)", DataDir: "/home/bob/domains/x/nextcloud-data",
	})
	cmd := exec.Command(php, shim)
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shim: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != root {
		t.Errorf("shim ran console.php from %q, want %q", out, root)
	}
	raw, err := os.ReadFile(filepath.Join(root, "argv.json"))
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	if err := json.Unmarshal(raw, &argv); err != nil {
		t.Fatal(err)
	}
	want := []string{root + "/occ", "maintenance:install", "--no-interaction", "--database=mysql",
		"--database-name=bob_nc", "--database-host=localhost", "--database-user=bob_nc",
		"--database-pass=p'a\"ss word", "--admin-user=zqxwvu", "--admin-pass=adm $(id)",
		"--data-dir=/home/bob/domains/x/nextcloud-data"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("occ argv = %q\nwant      %q", argv, want)
	}

	// A missing field stops it before occ runs.
	if err := os.Remove(filepath.Join(root, "argv.json")); err != nil {
		t.Fatal(err)
	}
	bad, _ := json.Marshal(map[string]string{"install_path": root})
	cmd = exec.Command(php, shim)
	cmd.Stdin = strings.NewReader(string(bad))
	if err := cmd.Run(); err == nil {
		t.Fatal("shim accepted an incomplete input")
	}
	if _, err := os.Stat(filepath.Join(root, "argv.json")); err == nil {
		t.Fatal("shim ran occ with an incomplete input")
	}
}

func TestNextcloudTopLevel_StaysInsideTheDocroot(t *testing.T) {
	for _, name := range nextcloudTopLevel {
		if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
			t.Errorf("unsafe delete entry %q", name)
		}
	}
}
