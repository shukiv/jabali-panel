package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const testDomainPHP = "/usr/bin/php8.4"

// envProgram splits an `env K=V… prog args…` command into its environment
// and the argv that env execs.
func envProgram(t *testing.T, argv []string) (map[string]string, []string) {
	t.Helper()
	if len(argv) == 0 || argv[0] != "env" {
		t.Fatalf("command %q does not start with env", argv)
	}
	vars := map[string]string{}
	i := 1
	for ; i < len(argv); i++ {
		k, v, ok := strings.Cut(argv[i], "=")
		if !ok || strings.HasPrefix(argv[i], "-") {
			break
		}
		vars[k] = v
	}
	return vars, argv[i:]
}

// assertComposerHomeOutsideSite: COMPOSER_HOME is the user's ~/.composer,
// not a directory in the site Composer runs for.
func assertComposerHomeOutsideSite(t *testing.T, home, installPath string) {
	t.Helper()
	if want := filepath.Join(homesRoot, "alice", ".composer"); home != want {
		t.Errorf("COMPOSER_HOME = %q, want %q", home, want)
	}
	if strings.HasPrefix(home, filepath.Join(homesRoot, "alice", "domains")+"/") || strings.HasPrefix(home, installPath+"/") {
		t.Errorf("COMPOSER_HOME %q is inside a web root", home)
	}
}

func TestDrupalComposerInstallRunsThePharOnTheDomainPHP(t *testing.T) {
	installPath := "/home/alice/domains/example.com/public_html"
	vars, prog := envProgram(t, drupalComposerInstallArgv("alice", testDomainPHP, "/usr/local/bin/composer-latest", installPath))
	if len(prog) < 3 || prog[0] != testDomainPHP || prog[1] != "/usr/local/bin/composer-latest" || prog[2] != "install" {
		t.Fatalf("composer runs as %q, want %s /usr/local/bin/composer-latest install …", prog, testDomainPHP)
	}
	if !containsString(prog, "--working-dir="+installPath) {
		t.Errorf("composer argv %q has no --working-dir=%s", prog, installPath)
	}
	assertComposerHomeOutsideSite(t, vars["COMPOSER_HOME"], installPath)
	if want := filepath.Join(vars["COMPOSER_HOME"], "cache"); vars["COMPOSER_CACHE_DIR"] != want {
		t.Errorf("COMPOSER_CACHE_DIR = %q, want %q", vars["COMPOSER_CACHE_DIR"], want)
	}
}

func TestDrushRunsOnTheSamePHPAsComposer(t *testing.T) {
	installPath := "/home/alice/domains/example.com/public_html"
	drush := filepath.Join(installPath, "vendor", "bin", "drush")

	run := drushSiteInstallRun(drupalInstallReq{
		OSUser: "alice", DBName: "db", DBUser: "dbu", DBPassword: "pw",
		SiteTitle: "Site", AdminUser: "admin", AdminPass: "pw", AdminEmail: "a@example.com",
	}, testDomainPHP, installPath)
	if run.PHP != testDomainPHP || run.Script != drush {
		t.Errorf("drush site:install runs %s %s, want %s %s", run.PHP, run.Script, testDomainPHP, drush)
	}
	assertComposerHomeOutsideSite(t, run.Env["COMPOSER_HOME"], installPath)

	vars, prog := envProgram(t, drushConfigSetArgv("alice", testDomainPHP, installPath, "css.preprocess"))
	if len(prog) < 3 || prog[0] != testDomainPHP || prog[1] != drush || prog[2] != "config:set" {
		t.Errorf("drush config:set runs as %q, want %s %s config:set …", prog, testDomainPHP, drush)
	}
	assertComposerHomeOutsideSite(t, vars["COMPOSER_HOME"], installPath)
}

const (
	testComposerDispatcher = "#!/bin/sh\n# jabali Composer version dispatcher\nexec /usr/local/bin/composer-latest \"$@\"\n"
	testComposerPhar       = "#!/usr/bin/env php\n<?php\nPhar::mapPhar('composer.phar');\n__HALT_COMPILER(); ?>\n"
)

func TestComposerPharForPicksThePharTheDispatcherWould(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string // name in the bin dir → content; all 0755
		noExec  []string          // files written 0644
		channel string            // "" = no choice file
		want    string            // file name in the bin dir; "" = error
	}{
		{"latest by default", map[string]string{"composer": testComposerDispatcher, "composer-latest": testComposerPhar, "composer-lts": testComposerPhar}, nil, "", "composer-latest"},
		{"lts channel", map[string]string{"composer": testComposerDispatcher, "composer-latest": testComposerPhar, "composer-lts": testComposerPhar}, nil, "lts", "composer-lts"},
		{"lts channel without lts installed", map[string]string{"composer": testComposerDispatcher, "composer-latest": testComposerPhar}, nil, "lts", "composer-latest"},
		{"latest channel", map[string]string{"composer": testComposerDispatcher, "composer-latest": testComposerPhar, "composer-lts": testComposerPhar}, nil, "latest", "composer-latest"},
		{"host before the dispatcher", map[string]string{"composer": testComposerPhar}, nil, "", "composer"},
		{"dispatcher without its phars", map[string]string{"composer": testComposerDispatcher}, nil, "", ""},
		{"composer-latest not executable", map[string]string{"composer": testComposerDispatcher, "composer-latest": testComposerPhar}, []string{"composer-latest"}, "", ""},
		{"nothing installed", nil, nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			choices := t.TempDir()
			prev := composerBinDir
			composerBinDir = bin
			t.Cleanup(func() { composerBinDir = prev })
			t.Setenv("JABALI_COMPOSER_CHOICE_ROOT", choices)
			for name, content := range tc.files {
				mode := os.FileMode(0o755)
				if containsString(tc.noExec, name) {
					mode = 0o644
				}
				if err := os.WriteFile(filepath.Join(bin, name), []byte(content), mode); err != nil {
					t.Fatal(err)
				}
			}
			if tc.channel != "" {
				if err := os.WriteFile(filepath.Join(choices, "alice"), []byte(tc.channel+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := composerPharFor("alice")
			if tc.want == "" {
				if err == nil {
					t.Fatalf("composerPharFor = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("composerPharFor: %v", err)
			}
			if want := filepath.Join(bin, tc.want); got != want {
				t.Errorf("composerPharFor = %q, want %q", got, want)
			}
		})
	}
}

func TestComposerPharForRejectsAnUnsafeUser(t *testing.T) {
	bin := t.TempDir()
	prev := composerBinDir
	composerBinDir = bin
	t.Cleanup(func() { composerBinDir = prev })
	t.Setenv("JABALI_COMPOSER_CHOICE_ROOT", t.TempDir())
	if err := os.WriteFile(filepath.Join(bin, "composer-latest"), []byte(testComposerPhar), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := composerPharFor("../etc"); err == nil {
		t.Fatalf("composerPharFor accepted ../etc (returned %q)", got)
	}
}

// The installer checks the domain's PHP before it downloads anything: a
// domain whose vhost passes no PHP has no PHP for Composer to run on.
func TestDrupalInstallNeedsTheDomainToRunPHP(t *testing.T) {
	sites := t.TempDir()
	prev := nginxSitesDir
	nginxSitesDir = sites
	t.Cleanup(func() { nginxSitesDir = prev })
	vhost := "server {\n  server_name example.com;\n  root /home/alice/domains/example.com/public_html;\n}\n"
	if err := os.WriteFile(filepath.Join(sites, "example.com.conf"), []byte(vhost), 0o644); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(drupalInstallReq{
		AppType: "drupal", OSUser: "alice", Docroot: "/home/alice/domains/example.com/public_html",
		SiteURL: "https://example.com", DBName: "db", DBUser: "dbu", DBPassword: "pw",
		SiteTitle: "Site", AdminUser: "admin", AdminPass: "pw", AdminEmail: "a@example.com",
	})
	_, err := drupalInstallHandler(context.Background(), params)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition || !strings.Contains(ae.Message, "does not run PHP") {
		t.Fatalf("drupalInstallHandler = %v, want failed_precondition: example.com does not run PHP", err)
	}
}

// ensureComposerHome runs as root in a home the tenant may own, so it makes
// only ~/.composer, refuses anything there that is not a directory, and
// never follows a link.
func TestEnsureComposerHome(t *testing.T) {
	me, err := user.Current()
	if err != nil || !osUserSafeRE.MatchString(me.Username) {
		t.Skipf("needs a current user with a POSIX name (%v)", err)
	}
	setup := func(t *testing.T) string {
		t.Helper()
		homes := t.TempDir()
		prev := homesRoot
		homesRoot = homes
		t.Cleanup(func() { homesRoot = prev })
		home := filepath.Join(homes, me.Username)
		if err := os.Mkdir(home, 0o755); err != nil {
			t.Fatal(err)
		}
		return home
	}

	t.Run("creates it owned by the user", func(t *testing.T) {
		home := setup(t)
		if err := ensureComposerHome(me.Username); err != nil {
			t.Fatalf("ensureComposerHome: %v", err)
		}
		fi, err := os.Lstat(filepath.Join(home, ".composer"))
		if err != nil || !fi.IsDir() {
			t.Fatalf("~/.composer is not a directory: %v", err)
		}
		if st := fi.Sys().(*syscall.Stat_t); strconv.Itoa(int(st.Uid)) != me.Uid {
			t.Errorf("~/.composer owned by uid %d, want %s", st.Uid, me.Uid)
		}
		if err := ensureComposerHome(me.Username); err != nil {
			t.Errorf("second ensureComposerHome: %v", err)
		}
	})
	t.Run("refuses a symlink", func(t *testing.T) {
		home := setup(t)
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(home, ".composer")); err != nil {
			t.Fatal(err)
		}
		if err := ensureComposerHome(me.Username); err == nil {
			t.Fatal("ensureComposerHome accepted a symlinked ~/.composer")
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("wrote through the link: %v", entries)
		}
	})
	t.Run("refuses a file", func(t *testing.T) {
		home := setup(t)
		if err := os.WriteFile(filepath.Join(home, ".composer"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ensureComposerHome(me.Username); err == nil {
			t.Fatal("ensureComposerHome accepted a file at ~/.composer")
		}
	})
}
