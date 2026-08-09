package main

import (
	"strings"
	"testing"
)

func TestExtractFPMFailureReason(t *testing.T) {
	cases := []struct {
		name    string
		journal string
		want    string // substring that must appear; "" means expect empty result
	}{
		{
			name: "ioncube loader not first / failed loading",
			journal: "Starting Jabali PHP-FPM (user elusuario)...\n" +
				"php-fpm: ERROR: Failed loading /usr/lib/php/20230831/ioncube_loader_lin_8.2.so:  /usr/lib/php/20230831/ioncube_loader_lin_8.2.so: cannot open shared object file\n" +
				"php-fpm: failed to post process the configuration\n",
			want: "Failed loading /usr/lib/php/20230831/ioncube_loader",
		},
		{
			name: "unknown pool directive",
			journal: "NOTICE: fpm is running, pid 12\n" +
				"ERROR: [pool jabali-alice] unknown entry 'php_admin_valu'\n" +
				"ERROR: failed to load configuration file '/etc/php/8.3/fpm/pool.d/jabali-alice.conf'\n",
			want: "unknown entry 'php_admin_valu'",
		},
		{
			name: "unable to load dynamic library",
			journal: "PHP Warning:  PHP Startup: Unable to load dynamic library 'redis.so' (tried: /usr/lib/php/.../redis.so ...)\n" +
				"[08-Aug-2026] NOTICE: ready to handle connections\n",
			want: "Unable to load dynamic library 'redis.so'",
		},
		{
			name: "prefers specific cause over trailing generic consequences",
			journal: "[08-Aug 21:26:02] ERROR: [/etc/php/8.4/fpm/pool.d/jabali-z.conf:71] unknown entry 'totally_bogus_xyz'\n" +
				"[08-Aug 21:26:02] ERROR: failed to load configuration file '/etc/jabali-panel/fpm/z.conf'\n" +
				"[08-Aug 21:26:02] ERROR: FPM initialization failed\n",
			want: "unknown entry 'totally_bogus_xyz'",
		},
		{
			name:    "healthy journal — no fatal",
			journal: "Starting Jabali PHP-FPM...\nNOTICE: fpm is running, pid 42\nNOTICE: ready to handle connections\n",
			want:    "",
		},
		{
			name:    "empty journal",
			journal: "",
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFPMFailureReason(tc.journal, 2)
			if tc.want == "" {
				if got != "" {
					t.Errorf("expected no reason, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("reason %q does not contain %q", got, tc.want)
			}
		})
	}
}

func TestExtractFPMFailureReason_NewestWinsAndDedup(t *testing.T) {
	journal := "ERROR: [pool jabali-x] unknown entry 'old_one'\n" +
		"ERROR: [pool jabali-x] unknown entry 'old_one'\n" + // dup
		"ERROR: [pool jabali-x] unknown entry 'new_one'\n"
	got := extractFPMFailureReason(journal, 2)
	// maxLines=2, newest-first scan, dedup → should include new_one and one old_one,
	// presented chronologically (old before new).
	if !strings.Contains(got, "new_one") {
		t.Errorf("most recent fatal missing: %q", got)
	}
	if strings.Count(got, "old_one") != 1 {
		t.Errorf("duplicate not collapsed: %q", got)
	}
	if strings.Index(got, "old_one") > strings.Index(got, "new_one") {
		t.Errorf("lines not chronological (old should precede new): %q", got)
	}
}

func TestDetectFPMMastersDown(t *testing.T) {
	// Fake the seams: two masters — one healthy, one crash-looping.
	origList, origShow, origJournal := fpmDiagListMasters, fpmDiagShow, fpmDiagJournal
	defer func() { fpmDiagListMasters, fpmDiagShow, fpmDiagJournal = origList, origShow, origJournal }()

	fpmDiagListMasters = func() (string, error) {
		return "jabali-fpm@alice.service loaded active running Jabali PHP-FPM (alice)\n" +
			"jabali-fpm@bob.service   loaded activating auto-restart Jabali PHP-FPM (bob)\n", nil
	}
	fpmDiagShow = func(unit string) (string, error) {
		if strings.Contains(unit, "alice") {
			return "ActiveState=active\nSubState=running\nResult=success\nNRestarts=0\nInvocationID=aaa\n", nil
		}
		return "ActiveState=activating\nSubState=auto-restart\nResult=exit-code\nNRestarts=7\nInvocationID=bbb\n", nil
	}
	fpmDiagJournal = func(unit string) string {
		if strings.Contains(unit, "bob") {
			return "ERROR: [pool jabali-bob] unknown entry 'bad_directive'\nERROR: failed to load configuration file\n"
		}
		return ""
	}

	broken, detail, err := detectFPMMastersDown(repairCtx{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !broken {
		t.Fatalf("expected broken=true (bob is down); detail=%q", detail)
	}
	if !strings.Contains(detail, "bob") || !strings.Contains(detail, "unknown entry 'bad_directive'") {
		t.Errorf("detail must name the down master + cause, got:\n%s", detail)
	}
	if strings.Contains(detail, "alice") {
		t.Errorf("healthy master must not be reported: %s", detail)
	}
	if !strings.Contains(detail, "7 restarts") {
		t.Errorf("expected restart evidence in reason, got:\n%s", detail)
	}
}

func TestDetectFPMMastersDown_AllHealthy(t *testing.T) {
	origList, origShow := fpmDiagListMasters, fpmDiagShow
	defer func() { fpmDiagListMasters, fpmDiagShow = origList, origShow }()
	fpmDiagListMasters = func() (string, error) {
		return "jabali-fpm@alice.service loaded active running Jabali PHP-FPM (alice)\n", nil
	}
	fpmDiagShow = func(unit string) (string, error) {
		return "ActiveState=active\nSubState=running\nResult=success\nNRestarts=0\nInvocationID=aaa\n", nil
	}
	broken, _, err := detectFPMMastersDown(repairCtx{})
	if err != nil || broken {
		t.Fatalf("expected healthy (broken=false, no err), got broken=%v err=%v", broken, err)
	}
}
