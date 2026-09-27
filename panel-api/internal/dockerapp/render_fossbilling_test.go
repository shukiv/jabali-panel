package dockerapp

import "testing"

// FOSSBilling — Apache+PHP app + bundled MariaDB + a one-shot installer
// that drives the web wizard headlessly. Verifies the template renders
// end-to-end with generated secrets, the seed init, the https system_url,
// the docker-range trusted proxy (force_https loop guard), the admin
// password policy suffix, and the idempotent installer guard.
func TestRender_FossBilling(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	a, ok := cat.Get("fossbilling")
	if !ok {
		t.Fatal("fossbilling missing from catalog")
	}
	out, err := Render(a, RenderParams{
		Slug: "fossbilling", Name: "billing", Domain: "billing.example.com",
		ImageChannel: "fossbilling/fossbilling:0.8.2",
		DataRoot:     "/var/lib/jabali/docker-apps/fossbilling",
		CPULimit:     "1.0", MemoryLimit: "512m",
		Ports: map[string]RuntimePort{"http": {HostPort: 10091, ContainerPort: 80, BindInterface: "127.0.0.1", Protocol: "tcp"}},
		Env: map[string]string{
			"DB_PASSWORD":                "dbpass",
			"MYSQL_ROOT_PASSWORD":        "rootpass",
			"FOSSBILLING_ADMIN_PASSWORD": "adminpw",
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertNoUnresolved(t, out)
	for _, n := range []string{
		"image: fossbilling/fossbilling:0.8.2",
		"image: mariadb:11",
		"init-seed:",
		"/var/lib/jabali/docker-apps/fossbilling/html:/seed",
		"--skip-old-files",
		"/var/lib/jabali/docker-apps/fossbilling/html:/var/www/html",
		"/var/lib/jabali/docker-apps/fossbilling/db:/var/lib/mysql",
		`"127.0.0.1:10091:80/tcp"`,
		"init-install:",
		"condition: service_completed_successfully",
		"condition: service_healthy",
		`if [ -f /seed/config.php ]; then`, // idempotency guard
		"system_url=https://billing.example.com/",
		"admin_email=admin@billing.example.com",
		`FB_ADMIN_PASSWORD: "adminpwAa1"`,      // policy suffix
		"admin_password=$${FB_ADMIN_PASSWORD}", // read from env, never spliced
		"trusted_proxy_proxies=172.16.0.0/12",  // force_https loop guard
		`FB_DB_PASSWORD: "dbpass"`,
		"database_password=$${FB_DB_PASSWORD}",
		`MYSQL_PASSWORD: "dbpass"`,
		`MYSQL_ROOT_PASSWORD: "rootpass"`,
	} {
		mustContain(t, out, n)
	}
}
