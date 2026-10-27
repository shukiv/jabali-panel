package cronvalidate

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateDeployCommands_Accepts(t *testing.T) {
	raw := "# migrate first\n" +
		"php artisan migrate --force\n" +
		"\n" +
		"php8.3 bin/console cache:clear --env=prod\n" +
		"php artisan tinker --execute='echo 1;'\n" +
		"wp cache flush\n" +
		"wp --path=web/wp plugin list\n" +
		"wp --path web/wp core version\n"
	cmds, err := ValidateDeployCommands(raw)
	if err != nil {
		t.Fatalf("ValidateDeployCommands: %v", err)
	}
	want := [][]string{
		{"php", "artisan", "migrate", "--force"},
		{"php8.3", "bin/console", "cache:clear", "--env=prod"},
		{"php", "artisan", "tinker", "--execute=echo 1;"},
		{"wp", "cache", "flush"},
		{"wp", "--path=web/wp", "plugin", "list"},
		{"wp", "--path", "web/wp", "core", "version"},
	}
	if len(cmds) != len(want) {
		t.Fatalf("got %d commands, want %d", len(cmds), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(cmds[i].Argv, want[i]) {
			t.Errorf("command %d = %q, want %q", i, cmds[i].Argv, want[i])
		}
	}
}

func TestValidateDeployCommands_EmptyIsValid(t *testing.T) {
	for _, raw := range []string{"", "\n\n", "# nothing yet\n"} {
		cmds, err := ValidateDeployCommands(raw)
		if err != nil || len(cmds) != 0 {
			t.Errorf("ValidateDeployCommands(%q) = %v, %v; want none and no error", raw, cmds, err)
		}
	}
}

func TestValidateDeployCommands_Refuses(t *testing.T) {
	for _, raw := range []string{
		"sh -c id",                          // not php or wp
		"composer install",                  // Composer is the deployment's own step
		"/usr/bin/php artisan migrate",      // php by path
		"php",                               // no file
		"php -n artisan migrate",            // PHP without php.ini, so without PHP Defense
		"php -c my.ini artisan migrate",     // another php.ini
		"php -d disable_functions= artisan", // an ini setting
		"php -r 'system(1);'",               // inline code
		"php -ddisable_functions= x.php",    // glued -d
		"php /home/u/x.php",                 // absolute file
		"php ../other/artisan",              // leaves the release
		"php ./artisan",                     // . segment
		"php artisan migrate; id",           // metacharacter
		"php artisan migrate && id",         // metacharacter
		"php artisan $(id)",                 // substitution
		"php artisan `id`",                  // backtick
		"php artisan migrate > /tmp/x",      // redirection
		"php 'artisan migrate",              // unclosed quote
		"php artisan\x00migrate",            // control character
		"php artisan\x1b[0m",                // control character
		"wp --path=/home/u/x plugin list",   // absolute --path
		"wp --path ../x plugin list",        // --path leaves the release
		"wp plugin list --path",             // --path without a value
		"php art%isan",                      // outside the path characters
	} {
		if _, err := ValidateDeployCommands(raw); err == nil {
			t.Errorf("ValidateDeployCommands(%q) accepted it", raw)
		}
	}
}

func TestValidateDeployCommands_NamesTheLine(t *testing.T) {
	_, err := ValidateDeployCommands("php artisan migrate --force\n\nsh -c id\n")
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("err = %v, want it to name line 3", err)
	}
}

func TestValidateDeployCommands_Limits(t *testing.T) {
	if _, err := ValidateDeployCommands(strings.Repeat("php artisan about\n", maxDeployCommands)); err != nil {
		t.Fatalf("%d commands: %v", maxDeployCommands, err)
	}
	if _, err := ValidateDeployCommands(strings.Repeat("php artisan about\n", maxDeployCommands+1)); err == nil {
		t.Fatalf("%d commands accepted", maxDeployCommands+1)
	}
	if _, err := ValidateDeployCommands("php artisan " + strings.Repeat("a", maxDeployCommandBytes)); err == nil {
		t.Fatalf("more than %d bytes accepted", maxDeployCommandBytes)
	}
}

func TestNormalizeDeployCommands(t *testing.T) {
	got := NormalizeDeployCommands("# c\n  php artisan migrate --force  \r\n\nwp cache flush\n")
	if want := "php artisan migrate --force\nwp cache flush"; got != want {
		t.Fatalf("NormalizeDeployCommands = %q, want %q", got, want)
	}
}

func TestValidateDeployLayout(t *testing.T) {
	ok := []struct {
		web    string
		shared []string
	}{
		{"public", []string{".env", "storage"}},
		{"", []string{"wp-content/uploads"}},
		{"web", []string{"web/uploads", "var/log"}},
		{"", nil},
	}
	for _, c := range ok {
		if err := ValidateDeployLayout(c.web, c.shared); err != nil {
			t.Errorf("ValidateDeployLayout(%q, %q): %v", c.web, c.shared, err)
		}
	}
	bad := []struct {
		web    string
		shared []string
	}{
		{"", []string{".env"}},              // a dotfile served from the web directory
		{"public", []string{"public/.env"}}, // the same, inside public
		{"public", []string{"public"}},      // the web directory itself
		{"public/app", []string{"public"}},  // holds the web directory
		{"public", []string{"storage", "storage/app"}},
		{"public", []string{".git"}},
		{"public", []string{"../x"}},
		{"public", []string{"/etc"}},
		{"../public", nil},
		{"pub lic", nil},
	}
	for _, c := range bad {
		if err := ValidateDeployLayout(c.web, c.shared); err == nil {
			t.Errorf("ValidateDeployLayout(%q, %q) accepted them", c.web, c.shared)
		}
	}
}
