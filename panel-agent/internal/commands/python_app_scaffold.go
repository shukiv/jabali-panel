// app.python.scaffold (JAB-164) — scaffold a framework starter project into a
// Python app's app_root, on top of the ADR-0131 runtime. Runs BEFORE the normal
// app.python.apply: it creates the venv, installs the pinned framework deps, lays
// down the starter (django-admin startproject / template files), hardens Django
// settings (SECRET_KEY from env, STATIC_ROOT, ALLOWED_HOSTS), and runs the
// framework post-install (migrate, collectstatic). Idempotent via a
// .jabali-scaffolded marker so a re-apply never re-scaffolds over the tenant's code.
//
// Everything that touches the app tree runs AS THE OWNING USER (sudo -u), same
// drop-priv boundary as app.python.apply. Generated secrets (Django SECRET_KEY)
// are returned to panel-api to persist in the app's env — never written to source.
//
// Every scaffold + serve command here was smoke-verified on Ubuntu 24.04/py3.12
// and Debian 13/py3.13 (Flask/Django/FastAPI → HTTP 200) before it was encoded.
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type pythonScaffoldParams struct {
	AppID         string   `json:"app_id"`
	Username      string   `json:"username"`
	AppRoot       string   `json:"app_root"`
	PythonVersion string   `json:"python_version"`
	Server        string   `json:"server"` // gunicorn|uvicorn|hypercorn|daphne
	Requirements  []string `json:"requirements"`
	ScaffoldKind  string   `json:"scaffold_kind"` // cmd|template
	// ScaffoldArgv is the scaffold command for kind=cmd, e.g.
	// ["django-admin","startproject","config","."]; argv[0] is resolved inside
	// the venv (django-admin ships with django once requirements install).
	ScaffoldArgv []string `json:"scaffold_argv,omitempty"`
	// TemplateFiles is relpath→content for kind=template starters (flask app.py …).
	TemplateFiles map[string]string `json:"template_files,omitempty"`
	// Project is the startproject dir (kind=cmd) — where settings.py lives.
	Project string `json:"project,omitempty"`
	// HardenDjango patches <project>/settings.py to read SECRET_KEY + ALLOWED_HOSTS
	// from env and set STATIC_ROOT, then generates a SECRET_KEY into GeneratedEnv.
	HardenDjango bool   `json:"harden_django,omitempty"`
	StaticRoot   string `json:"static_root,omitempty"`
	AllowedHosts string `json:"allowed_hosts,omitempty"`
	// PatchScript is python run AS TENANT in app_root after scaffold + deps and
	// before post-install, with Env + generated secrets exported. Frameworks
	// whose settings layout differs from a plain config/settings.py (Wagtail's
	// settings package, Channels' asgi router) use it for their own surgery,
	// keeping framework specifics in the catalog instead of the agent.
	PatchScript string `json:"patch_script,omitempty"`
	// Env is the resolved non-secret environment exposed to the patch script +
	// post-install (DJANGO_SETTINGS_MODULE, DJANGO_ALLOWED_HOSTS, JAB_STATIC_ROOT…).
	Env map[string]string `json:"env,omitempty"`
	// Generate lists env keys the agent mints a strong random secret for
	// (returned in GeneratedEnv to persist; never written to source).
	Generate    []string `json:"generate,omitempty"`
	PostInstall []string `json:"post_install,omitempty"` // migrate, collectstatic
}

type pythonScaffoldResult struct {
	Scaffolded   bool              `json:"scaffolded"`
	Skipped      bool              `json:"skipped"`
	GeneratedEnv map[string]string `json:"generated_env,omitempty"`
}

var scaffoldServers = map[string]bool{"gunicorn": true, "uvicorn": true, "hypercorn": true, "daphne": true}

func scaffoldErr(format string, a ...any) error {
	return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf(format, a...)}
}

func pythonAppScaffoldHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p pythonScaffoldParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg("parse params: " + err.Error())
	}
	// The agent is the security boundary — re-validate the caller's values even
	// though panel-api sends ULIDs + validated names.
	if !appIDRe.MatchString(p.AppID) {
		return nil, csInvalidArg("bad app_id")
	}
	if !usernameRe.MatchString(p.Username) {
		return nil, csInvalidArg("bad username")
	}
	if !pythonVersionRe.MatchString(p.PythonVersion) {
		return nil, csInvalidArg("bad python_version")
	}
	if !scaffoldServers[p.Server] {
		return nil, csInvalidArg("bad server")
	}
	if !filepath.IsAbs(p.AppRoot) || strings.Contains(p.AppRoot, "..") || hasCtrl(p.AppRoot) {
		return nil, csInvalidArg("bad app_root")
	}
	if p.ScaffoldKind != "cmd" && p.ScaffoldKind != "template" {
		return nil, csInvalidArg("bad scaffold_kind")
	}
	if len(p.Requirements) == 0 {
		return nil, csInvalidArg("requirements required")
	}

	marker := filepath.Join(p.AppRoot, ".jabali-scaffolded")
	if _, err := os.Stat(marker); err == nil {
		return pythonScaffoldResult{Skipped: true}, nil
	}

	venv := filepath.Join(p.AppRoot, "venv")
	pyBin := "/usr/bin/python" + p.PythonVersion

	// 1) venv (GH#357: python<ver>-venv package prerequisite).
	if _, err := os.Stat(filepath.Join(venv, "bin", "python")); err != nil {
		if verr := ensurePythonVenvPackage(ctx, p.PythonVersion); verr != nil {
			return nil, scaffoldErr("venv prerequisite: %v", verr)
		}
		if out, err := runAsUser(ctx, p.Username, pyBin, "-m", "venv", venv); err != nil {
			return nil, scaffoldErr("create venv: %v: %s", err, out)
		}
	}
	pip := filepath.Join(venv, "bin", "pip")

	// 2) install the pinned deps (framework + server) BY NAME, not via a
	//    requirements.txt file: some scaffolders (`wagtail start`) refuse to run
	//    in a directory that already contains requirements.txt. We write the file
	//    after scaffolding (step 3b) so app.python.apply still has it.
	pipArgs := append([]string{pip, "install", "-q"}, p.Requirements...)
	pipArgs = append(pipArgs, p.Server)
	if out, err := runAsUser(ctx, p.Username, pipArgs...); err != nil {
		return nil, scaffoldErr("pip install: %v: %s", err, lastLines(out, 12))
	}

	// 3) scaffold the starter.
	switch p.ScaffoldKind {
	case "cmd":
		if len(p.ScaffoldArgv) == 0 {
			return nil, csInvalidArg("scaffold_argv required for cmd")
		}
		// Resolve argv[0] inside the venv (django-admin ships with django).
		bin := filepath.Join(venv, "bin", filepath.Base(p.ScaffoldArgv[0]))
		args := append([]string{bin}, p.ScaffoldArgv[1:]...)
		// Run IN app_root: scaffolders use relative targets (e.g. `startproject
		// config .`), so cwd must be the app tree or files land in the agent's cwd.
		if out, err := runAsUserInDir(ctx, p.Username, p.AppRoot, args...); err != nil {
			return nil, scaffoldErr("scaffold: %v: %s", err, lastLines(out, 8))
		}
	case "template":
		for rel, content := range p.TemplateFiles {
			if strings.Contains(rel, "..") || filepath.IsAbs(rel) || hasCtrl(rel) {
				return nil, csInvalidArg("bad template path " + rel)
			}
			dst := filepath.Join(p.AppRoot, rel)
			if dir := filepath.Dir(dst); dir != p.AppRoot {
				if _, err := runAsUser(ctx, p.Username, "mkdir", "-p", dir); err != nil {
					return nil, scaffoldErr("mkdir %s: %v", dir, err)
				}
			}
			if err := writeAsUser(ctx, p.Username, dst, content); err != nil {
				return nil, scaffoldErr("write %s: %v", rel, err)
			}
		}
	}

	// 3b) record the pinned deps now that the scaffolder has run — app.python.apply
	//     reinstalls from this file. Overwrites any requirements.txt a scaffolder
	//     generated (e.g. wagtail's) with our exact pins.
	reqBody := strings.Join(append(append([]string(nil), p.Requirements...), p.Server), "\n") + "\n"
	if werr := writeAsUser(ctx, p.Username, filepath.Join(p.AppRoot, "requirements.txt"), reqBody); werr != nil {
		return nil, scaffoldErr("write requirements.txt: %v", werr)
	}

	result := pythonScaffoldResult{Scaffolded: true, GeneratedEnv: map[string]string{}}

	// 4) Django hardening: patch settings, generate SECRET_KEY.
	if p.HardenDjango {
		if !projectDirRe.MatchString(p.Project) {
			return nil, csInvalidArg("bad django project")
		}
		settings := filepath.Join(p.AppRoot, p.Project, "settings.py")
		vpy := filepath.Join(venv, "bin", "python")
		if out, err := runAsUser(ctx, p.Username, vpy, "-c", djangoSettingsPatch, settings, p.StaticRoot); err != nil {
			return nil, scaffoldErr("harden settings: %v: %s", err, out)
		}
		key, err := runAsUser(ctx, p.Username, vpy, "-c", "from django.core.management.utils import get_random_secret_key as g;print(g())")
		if err != nil {
			return nil, scaffoldErr("generate secret key: %v: %s", err, key)
		}
		result.GeneratedEnv["DJANGO_SECRET_KEY"] = strings.TrimSpace(key)
	}

	// 4b) Generic patch-script hardening for frameworks whose settings layout
	// isn't a plain config/settings.py (Wagtail's package, Channels' asgi router).
	// The agent mints the secrets generically (stdlib, no framework dependency)
	// and runs the entry's own python patch AS TENANT with the full env exported.
	if strings.TrimSpace(p.PatchScript) != "" {
		vpy := filepath.Join(venv, "bin", "python")
		for _, key := range p.Generate {
			if key == "" {
				continue
			}
			v, err := runAsUser(ctx, p.Username, vpy, "-c", "import secrets;print(secrets.token_urlsafe(50))")
			if err != nil {
				return nil, scaffoldErr("generate %s: %v: %s", key, err, v)
			}
			result.GeneratedEnv[key] = strings.TrimSpace(v)
		}
		patchPath := filepath.Join(p.AppRoot, ".jabali-patch.py")
		if err := writeAsUser(ctx, p.Username, patchPath, p.PatchScript); err != nil {
			return nil, scaffoldErr("write patch: %v", err)
		}
		args := append(envPrefix(mergeScaffoldEnv(p.Env, result.GeneratedEnv)), vpy, patchPath)
		if out, err := runAsUserInDir(ctx, p.Username, p.AppRoot, args...); err != nil {
			return nil, scaffoldErr("patch: %v: %s", err, lastLines(out, 10))
		}
		_, _ = runAsUser(ctx, p.Username, "rm", "-f", patchPath)
	}

	// 5) post-install (with the generated + resolved env in scope for
	// migrate/collectstatic — e.g. DJANGO_SETTINGS_MODULE for Wagtail).
	if len(p.PostInstall) > 0 {
		penv := mergeScaffoldEnv(p.Env, result.GeneratedEnv)
		if p.AllowedHosts != "" {
			penv["DJANGO_ALLOWED_HOSTS"] = p.AllowedHosts
		}
		envArgs := envPrefix(penv)
		vpy := filepath.Join(venv, "bin", "python")
		manage := filepath.Join(p.AppRoot, "manage.py")
		for _, step := range p.PostInstall {
			var sub string
			switch step {
			case "migrate":
				sub = "migrate"
			case "collectstatic":
				sub = "collectstatic"
			default:
				continue // ignore unknown tokens; the catalog validates the set
			}
			args := append(append([]string(nil), envArgs...), vpy, manage, sub, "--noinput")
			if out, err := runAsUserInDir(ctx, p.Username, p.AppRoot, args...); err != nil {
				return nil, scaffoldErr("%s: %v: %s", step, err, lastLines(out, 8))
			}
		}
	}

	// 6) marker (idempotency) — written last so a mid-scaffold failure re-runs clean.
	if err := writeAsUser(ctx, p.Username, marker, "scaffolded\n"); err != nil {
		return nil, scaffoldErr("write marker: %v", err)
	}
	return result, nil
}

// projectDirRe guards the Django project dir name (django-admin startproject
// <name>) — it becomes a Python package + a filesystem path segment.
var projectDirRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// djangoSettingsPatch reads argv [settings_path, static_root] and rewrites a
// fresh startproject settings.py to read SECRET_KEY + ALLOWED_HOSTS from env and
// set STATIC_ROOT (matching the smoke-verified hardening).
const djangoSettingsPatch = `import re,sys
path,static_root=sys.argv[1],sys.argv[2]
s=open(path).read()
if "\nimport os" not in s and not s.startswith("import os"):
    s="import os\n"+s
s=re.sub(r"^SECRET_KEY = .*$","SECRET_KEY = os.environ['DJANGO_SECRET_KEY']",s,flags=re.M)
s=re.sub(r"^ALLOWED_HOSTS = .*$","ALLOWED_HOSTS = os.environ.get('DJANGO_ALLOWED_HOSTS','').split(',')",s,flags=re.M)
if "STATIC_ROOT" not in s:
    s=s.rstrip()+"\nSTATIC_ROOT = BASE_DIR / %r\n" % static_root
open(path,"w").write(s)
`

// writeAsUser writes a file inside the tenant's app tree AS THE TENANT.
//
// The agent runs as root, so a plain root os.WriteFile into the tenant-owned
// app tree is a privilege-escalation primitive: the tenant can pre-plant a
// symlink at `path` pointing anywhere on the host, and the root write follows
// it (symlink-following TOCTOU). Streaming the content through `sudo -u <user>
// tee` performs the open+write under the tenant uid, so the kernel enforces DAC
// on the (possibly symlinked) target — a tenant can only ever redirect the
// write to a path the tenant could already write, which is not an escalation.
// It also lands the file owned by the tenant with no separate chown (the venv +
// code must be user-owned for the loopback unit that runs as that user).
//
// `--` guards a path beginning with '-'; app_root is always absolute so this is
// belt-and-suspenders. No shell is involved.
func writeAsUser(ctx context.Context, username, path, content string) error {
	cmd := exec.CommandContext(ctx, "sudo", "-u", username, "-H", "tee", "--", path)
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = io.Discard // tee echoes stdin to stdout; we don't need it
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write as %s: %w: %s", username, err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// mergeScaffoldEnv returns a new map of the resolved env overlaid with generated
// secrets (secrets win). Never mutates its inputs.
func mergeScaffoldEnv(env, generated map[string]string) map[string]string {
	out := make(map[string]string, len(env)+len(generated))
	for k, v := range env {
		out[k] = v
	}
	for k, v := range generated {
		out[k] = v
	}
	return out
}

// envPrefix builds a deterministic `env K=V …` argv prefix (sorted keys) for a
// no-shell `sudo -u <user> env …` invocation.
func envPrefix(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys)+1)
	out = append(out, "env")
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func init() { Default.Register("app.python.scaffold", pythonAppScaffoldHandler) }
