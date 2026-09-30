package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const repoPoolTemplate = "../../../install/php/jabali-php-pool.conf.tmpl"

// The repo's pool template sets the shared-hosting defaults every pool runs
// with (GH #253). If a {{ if }} ever wraps them, the baseline would fall back
// to php.ini's 128M and every unset domain would be pinned to it.
func TestPoolTemplateLiteralDefaults(t *testing.T) {
	got := readPoolTemplateIniValues(repoPoolTemplate)
	for d, want := range map[string]string{
		"memory_limit":        "512M",
		"upload_max_filesize": "512M",
		"post_max_size":       "512M",
		"max_execution_time":  "300",
		"max_input_time":      "300",
		"max_input_vars":      "10000",
	} {
		if got[d] != want {
			t.Errorf("template %s = %q, want %q", d, got[d], want)
		}
	}
	for d := range got {
		if strings.Contains(got[d], "{{") {
			t.Errorf("templated line leaked into the literal values: %s=%q", d, got[d])
		}
	}
}

func TestReadPoolTemplateIniValues_AdminBeatsValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.tmpl")
	body := strings.Join([]string{
		"php_admin_value[memory_limit] = 1G",
		"php_value[memory_limit] = 512M",
		"  php_value[max_input_vars] = 5000  ",
		"php_admin_value[{{.Name}}] = {{.Value}}",
		"php_value[date.timezone] = {{ .TZ }}",
		"; php_value[post_max_size] = 9M",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readPoolTemplateIniValues(path)
	want := map[string]string{"memory_limit": "1G", "max_input_vars": "5000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if readPoolTemplateIniValues(filepath.Join(t.TempDir(), "missing")) != nil {
		t.Fatal("an unreadable template must yield nil")
	}
}

// The CLI read reports php.ini (128M) and the CLI SAPI's forced 0 / -1; the
// template's values win, and a CLI-forced directive the template does not set
// is dropped rather than reported as 0.
func TestOverlayPoolTemplateDefaults(t *testing.T) {
	cli := map[string]string{
		"memory_limit": "128M", "max_execution_time": "0", "max_input_time": "-1",
		"error_reporting": "22527", "log_errors": "1",
	}
	got := overlayPoolTemplateDefaults(cli, map[string]string{"memory_limit": "512M", "max_execution_time": "300", "max_file_uploads": "100"})
	want := map[string]string{"memory_limit": "512M", "max_execution_time": "300", "error_reporting": "22527", "log_errors": "1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if cli["memory_limit"] != "128M" {
		t.Fatal("the CLI map must not be mutated")
	}
}

// End to end through php.ini_defaults: a stubbed CLI read that says what the
// box's php CLI says, plus the real repo template, gives the FPM values.
func TestReadPHPIniDefaults_UsesThePoolTemplate(t *testing.T) {
	t.Setenv("JABALI_PHP_POOL_TEMPLATE_PATH", repoPoolTemplate)
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "%s", `{"memory_limit":"128M","upload_max_filesize":"2M","post_max_size":"8M","max_input_vars":"1000","max_execution_time":"0","max_input_time":"-1","error_reporting":"22527","date.timezone":"","log_errors":"1","file_uploads":"1","short_open_tag":""}`)
	}
	t.Cleanup(func() { execCommandContext = prev })

	got, err := readPHPIniDefaults(context.Background(), "8.4")
	if err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]string{
		"memory_limit": "512M", "upload_max_filesize": "512M", "post_max_size": "512M", "max_input_vars": "10000",
		"max_execution_time": "300", "max_input_time": "300", "error_reporting": "22527", "date.timezone": "", "log_errors": "1",
	} {
		if v, ok := got[d]; !ok || v != want {
			t.Errorf("%s = %q (present %v), want %q", d, v, ok, want)
		}
	}
}

func valuePinParams() *domainCreateParams {
	return &domainCreateParams{Domain: "a.example", HasPHP: true, PHPVersion: "8.4"}
}

// A domain that sets a value keeps it; one it leaves unset is pinned to the
// pool override, else the baseline. An empty timezone runs as UTC; an empty
// error_reporting and a baseline without a directive are not pinned.
func TestPHPInheritedValuePins_Sources(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{
		"memory_limit": "512M", "upload_max_filesize": "512M", "post_max_size": "512M",
		"max_input_vars": "10000", "max_input_time": "300", "error_reporting": "", "date.timezone": "",
	}, nil)
	p := valuePinParams()
	p.PHPMemoryLimit = "256M"
	p.PHPMaxInputVars = 3000
	p.PHPPoolValues = map[string]string{"upload_max_filesize": "1G", "memory_limit": "2G"}
	got := phpInheritedValuePins(context.Background(), p)
	want := []string{"upload_max_filesize=1G", "post_max_size=512M", "max_input_time=300", "date.timezone=UTC"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// With the pool's overrides unknown, nothing is pinned from the baseline (it
// could defeat an admin override); the values the panel did send still pin.
func TestPHPInheritedValuePins_InheritUnknown(t *testing.T) {
	calls := stubPHPIniBaseline(t, map[string]string{"memory_limit": "512M"}, nil)
	p := valuePinParams()
	p.PHPFlagsInheritUnknown = true
	p.PHPPoolValues = map[string]string{"max_input_vars": "20000"}
	got := phpInheritedValuePins(context.Background(), p)
	if !reflect.DeepEqual(got, []string{"max_input_vars=20000"}) || *calls != 0 {
		t.Fatalf("got %v with %d baseline reads, want only max_input_vars and no read", got, *calls)
	}
}

// Every pinned value lands inside fastcgi_param PHP_VALUE "..."; a value that
// is not a plain size, integer or zone is not pinned.
func TestPHPInheritedValuePins_RefusesUnsafeValues(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{"memory_limit": `512M"; x`, "error_reporting": "E_ALL", "max_input_vars": "10000"}, nil)
	p := valuePinParams()
	p.PHPPoolValues = map[string]string{"date.timezone": "Europe/Berlin\nphp_admin_value[open_basedir]=/", "upload_max_filesize": "-1"}
	got := phpInheritedValuePins(context.Background(), p)
	want := []string{"upload_max_filesize=-1", "max_input_vars=10000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestPHPInheritedValuePins_NonPHPAndBaselineFailure(t *testing.T) {
	stubPHPIniBaseline(t, nil, os.ErrNotExist)
	p := valuePinParams()
	p.HasPHP = false
	if got := phpInheritedValuePins(context.Background(), p); got != nil {
		t.Fatalf("non-PHP got %v", got)
	}
	p.HasPHP = true
	p.PHPPoolValues = map[string]string{"memory_limit": "1G"}
	if got := phpInheritedValuePins(context.Background(), p); !reflect.DeepEqual(got, []string{"memory_limit=1G"}) {
		t.Fatalf("baseline failure got %v, want only the pool value", got)
	}
}

// The PHP_VALUE line of a domain that sets nothing carries every inherited
// value once, next to display_errors and the flags; a domain value is never
// followed by an inherited one for the same directive.
func TestPHPValueLine_PinsInheritedValues(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{
		"memory_limit": "512M", "upload_max_filesize": "512M", "post_max_size": "512M", "max_input_vars": "10000",
		"max_execution_time": "300", "max_input_time": "300", "error_reporting": "22527", "date.timezone": "",
		"log_errors": "1", "file_uploads": "1", "short_open_tag": "",
	}, nil)
	p := valuePinParams()
	p.PHPMemoryLimit = "64M"
	line := withPHPFlagPins(buildPHPValueParam(true, p.PHPMemoryLimit, "", "", 0, 0, 0, false, nil, ""), phpFlagPinsForParams(context.Background(), p))
	for _, want := range []string{"display_errors=Off", "memory_limit=64M", "upload_max_filesize=512M", "post_max_size=512M",
		"max_input_vars=10000", "max_execution_time=300", "max_input_time=300", "error_reporting=22527", "date.timezone=UTC",
		"log_errors=On", "file_uploads=On", "short_open_tag=Off"} {
		if !strings.Contains(line, want) {
			t.Errorf("PHP_VALUE %q lacks %q", line, want)
		}
	}
	if n := strings.Count(line, "memory_limit="); n != 1 {
		t.Fatalf("memory_limit appears %d times in %q", n, line)
	}
}
