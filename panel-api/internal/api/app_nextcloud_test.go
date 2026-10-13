package api

import (
	"context"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
)

// Discussion #1032: the Nextcloud kicker hands the agent every install input,
// seeds the admin username the service stored (the one the UI shows), and
// returns the admin password it seeded.
func TestNextcloudKicker_SendsInstallInputs(t *testing.T) {
	ag := &kickerAgentFake{done: make(chan struct{})}
	deps := ApplicationHandlerConfig{
		ApplicationInstalls: &kickerInstallsFake{},
		Agent:               ag,
		// CronJobs and CRSExclusions nil: cron creation and the WAF sync are
		// skipped inside the kicker.
	}
	pass := dispatchInstallKicker(context.Background(), "nextcloud", kickContext{
		InstallID:     "01TESTINSTALLID000000000000",
		OSUser:        "bob",
		DocRoot:       "/home/bob/domains/cloud.example.com/public_html",
		SiteURL:       "https://cloud.example.com",
		AdminUsername: "zqxwvu",
		AdminEmail:    "admin@example.com",
		Chain:         provisionedDB{DBName: "bob_nc", DBUsername: "bob_nc", DBPassword: "dbsecret"},
		Params:        map[string]any{},
	}, deps)

	select {
	case <-ag.done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent app.install was never called")
	}
	ag.mu.Lock()
	defer ag.mu.Unlock()
	want := map[string]any{
		"app_type":    "nextcloud",
		"os_user":     "bob",
		"docroot":     "/home/bob/domains/cloud.example.com/public_html",
		"site_url":    "https://cloud.example.com",
		"db_name":     "bob_nc",
		"db_user":     "bob_nc",
		"db_password": "dbsecret",
		"db_host":     "localhost",
		"admin_user":  "zqxwvu",
		"admin_email": "admin@example.com",
	}
	for k, v := range want {
		if ag.params[k] != v {
			t.Errorf("agent param %s = %v, want %v", k, ag.params[k], v)
		}
	}
	if pass == "" || ag.params["admin_pass"] != pass {
		t.Fatalf("returned admin password %q must be what the agent seeds (%v)", pass, ag.params["admin_pass"])
	}
}

// The background-job cron pins the PHP version the install's pool runs, and an
// unexpected version string can never reach the command.
func TestNextcloudCronCommand(t *testing.T) {
	const installPath = "/home/u1/domains/cloud.example.com/public_html"
	for _, tc := range []struct{ ver, want string }{
		{"8.4", "php8.4 " + installPath + "/cron.php"},
		{"8.3", "php8.3 " + installPath + "/cron.php"},
		{"", "php " + installPath + "/cron.php"},
		{"8", "php " + installPath + "/cron.php"},
		{"8.", "php " + installPath + "/cron.php"},
		{"8.4; rm -rf ~", "php " + installPath + "/cron.php"},
		{"x.y", "php " + installPath + "/cron.php"},
	} {
		got := nextcloudCronCommand(installPath, tc.ver)
		if got != tc.want {
			t.Errorf("version %q: got %q, want %q", tc.ver, got, tc.want)
		}
		if _, err := cronvalidate.ValidateCommand(got, []string{installPath}, ""); err != nil {
			t.Errorf("version %q: command %q rejected by the cron allowlist: %v", tc.ver, got, err)
		}
	}
}

// Moodle, osTicket and Nextcloud create cron jobs at install, so their delete
// must tear them down too. The switch used to cover only WordPress, Joomla and
// MediaWiki, which left the others' jobs behind to fail on every run.
func TestRunAppDelete_TearsDownCronsOfAppsThatCreateThem(t *testing.T) {
	for _, app := range []string{"wordpress", "joomla", "mediawiki", "moodle", "osticket", "nextcloud"} {
		_, _, _, _, _, cr, deps := newAppDeleteFakes()
		args := appDeleteArgs()
		args.AppType = app
		if err := RunAppDelete(args, deps); err != nil {
			t.Fatalf("%s: %v", app, err)
		}
		if !cr.listed {
			t.Errorf("%s: cron teardown did not run", app)
		}
	}
}
