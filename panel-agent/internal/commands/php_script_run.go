package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

// php_script_run.go — run a PHP CLI script as a tenant with its arguments
// and environment passed on stdin.
//
// The one-click installers run each app's own CLI installer as the tenant
// through `systemd-run`, and those installers take the database password and
// the admin password as options. A transient unit's command line and
// environment are unit properties, not private to the process, so no
// credential goes there.
//
// So the unit runs `php <flags> <shim>` and nothing else. The shim reads the
// script path, its arguments and its environment as JSON on stdin, sets
// $argv, $_SERVER['argv'] and the environment in-process, and requires the
// script, as `php <script> <args>` would have run it.
//
// It does not work for a script that parses options with getopt(), which
// reads the real process argv (ITFlow's setup_cli.php has its own route).

const phpScriptShim = `<?php
// Jabali PHP script runner. Reads {"script","args","env"} as JSON on stdin
// and runs the script with that argv and environment, so no argument is on a
// command line.
if (PHP_SAPI !== "cli") { exit(2); }
$jabali_in = json_decode(stream_get_contents(STDIN), true);
if (!is_array($jabali_in) || !isset($jabali_in["script"]) || !is_string($jabali_in["script"]) || substr($jabali_in["script"], 0, 1) !== "/") {
  fwrite(STDERR, "jabali-php-run: unreadable input\n"); exit(2);
}
$jabali_args = [$jabali_in["script"]];
foreach (($jabali_in["args"] ?? []) as $jabali_a) {
  if (!is_string($jabali_a)) { fwrite(STDERR, "jabali-php-run: bad argument\n"); exit(2); }
  $jabali_args[] = $jabali_a;
}
foreach (($jabali_in["env"] ?? []) as $jabali_k => $jabali_v) {
  if (!is_string($jabali_k) || !preg_match('/^[A-Za-z_][A-Za-z0-9_]*$/', $jabali_k) || !is_string($jabali_v)) {
    fwrite(STDERR, "jabali-php-run: bad environment\n"); exit(2);
  }
  putenv($jabali_k . "=" . $jabali_v);
  $_ENV[$jabali_k] = $jabali_v;
  $_SERVER[$jabali_k] = $jabali_v;
}
$_SERVER["argv"] = $jabali_args;
$_SERVER["argc"] = count($jabali_args);
$_SERVER["SCRIPT_FILENAME"] = $jabali_args[0];
$_SERVER["SCRIPT_NAME"] = $jabali_args[0];
$_SERVER["PHP_SELF"] = $jabali_args[0];
$_SERVER["PATH_TRANSLATED"] = $jabali_args[0];
$argv = $jabali_args;
$argc = count($jabali_args);
unset($jabali_in, $jabali_args, $jabali_a, $jabali_k, $jabali_v);
require $argv[0];
`

// phpShimStagingDir makes the directory a shim is written to; tests point
// it at a temporary directory.
var phpShimStagingDir = stagingMkdirTemp

// phpScriptEnvKeyRE is an environment variable name the shim accepts.
var phpScriptEnvKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// phpScriptRun is one run of a PHP CLI script as a tenant. Only OSUser, PHP
// and PHPFlags reach the unit's command line; Script, Args and Env travel on
// stdin, so Args and Env may carry secrets.
type phpScriptRun struct {
	OSUser   string
	PHP      string   // the PHP binary, e.g. "php8.4"
	PHPFlags []string // -d flags for the PHP binary; never secret
	Script   string   // absolute path of the script
	Args     []string // the script's arguments, as after `php <script>`
	Env      map[string]string
	Dir      string // working directory; "" keeps the agent's
}

// phpScriptInput is what the shim reads on stdin.
type phpScriptInput struct {
	Script string            `json:"script"`
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env,omitempty"`
}

// cmd builds the systemd-run command that runs r through the shim at
// shimPath.
func (r phpScriptRun) cmd(ctx context.Context, shimPath string) (*exec.Cmd, error) {
	if !filepath.IsAbs(r.Script) {
		return nil, fmt.Errorf("php script path %q is not absolute", r.Script)
	}
	for k := range r.Env {
		if !phpScriptEnvKeyRE.MatchString(k) {
			return nil, fmt.Errorf("invalid environment variable name %q", k)
		}
	}
	args := r.Args
	if args == nil {
		args = []string{}
	}
	in, err := json.Marshal(phpScriptInput{Script: r.Script, Args: args, Env: r.Env})
	if err != nil {
		return nil, err
	}
	argv := append([]string{r.PHP}, r.PHPFlags...)
	argv = append(argv, shimPath)
	cmd := buildSystemdRunCmd(ctx, r.OSUser, argv...)
	cmd.Dir = r.Dir
	cmd.Stdin = bytes.NewReader(in)
	return cmd, nil
}

// run runs r through the shim with runBoundedOutput.
func (r phpScriptRun) run(ctx context.Context) ([]byte, error) {
	return runStagedPHPShim("jabali-php-run.php", phpScriptShim, func(shimPath string) (*exec.Cmd, error) {
		return r.cmd(ctx, shimPath)
	})
}

// runStagedPHPShim writes shim, as name, to a fresh staging directory the
// tenant can read, runs the command build makes for it with
// runBoundedOutput, and removes the shim.
func runStagedPHPShim(name, shim string, build func(shimPath string) (*exec.Cmd, error)) ([]byte, error) {
	dir, err := phpShimStagingDir("php-run-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	shimPath := filepath.Join(dir, name)
	if err := os.WriteFile(shimPath, []byte(shim), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", name, err)
	}
	cmd, err := build(shimPath)
	if err != nil {
		return nil, err
	}
	return runBoundedOutput(cmd, 0)
}
