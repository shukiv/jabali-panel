package dockerapp

import (
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type rocketchatService struct {
	Image     string                    `yaml:"image"`
	Restart   string                    `yaml:"restart"`
	Command   []string                  `yaml:"command"`
	Volumes   []string                  `yaml:"volumes"`
	DependsOn map[string]map[string]any `yaml:"depends_on"`
}

func renderRocketChat(t *testing.T) map[string]rocketchatService {
	t.Helper()
	var doc struct {
		Services map[string]rocketchatService `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(renderDrill(t, "rocketchat")), &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc.Services
}

// Rocket.Chat 8 refuses MongoDB 6.0, which the catalog kept after the move to
// 8.x. MongoDB 8.0 runs on a new mongo8/ that two one-shots fill once from
// the 6.0 database in mongo/, and mongo/ itself is only read.
func TestRocketChat_MongoDB8RunsOnACopyOfTheOldDatabase(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	e, _ := cat.Get("rocketchat")
	if !e.StopBeforeRecreate {
		t.Fatal("rocketchat must stop its old containers before the copy reads mongo/")
	}
	svc := renderRocketChat(t)
	root := "/var/lib/jabali/docker-apps/rocketchat/"

	db := svc["mongodb"]
	if !strings.HasPrefix(db.Image, "mongo:8.0@sha256:") {
		t.Errorf("mongodb image %q, want mongo:8.0", db.Image)
	}
	if len(db.Volumes) != 1 || db.Volumes[0] != root+"mongo8:/data/db" {
		t.Errorf("mongodb mounts %v, want only mongo8/", db.Volumes)
	}
	export, imp := svc["mongo-export"], svc["mongo-import"]
	if !strings.HasPrefix(export.Image, "mongo:6.0@sha256:") {
		t.Errorf("mongo-export image %q, want mongo:6.0 (8.0 cannot open a 6.0 database)", export.Image)
	}
	if imp.Image != db.Image {
		t.Errorf("mongo-import image %q, want the mongodb image %q", imp.Image, db.Image)
	}
	if !containsString(export.Volumes, root+"mongo:/data/db") || !containsString(export.Volumes, root+"mongo8:/mongo8:ro") {
		t.Errorf("mongo-export mounts %v", export.Volumes)
	}
	if !containsString(imp.Volumes, root+"mongo8:/data/db") {
		t.Errorf("mongo-import mounts %v", imp.Volumes)
	}
	for name, s := range map[string]rocketchatService{"mongo-export": export, "mongo-import": imp} {
		if s.Restart != "no" {
			t.Errorf("%s restart %q, want \"no\"", name, s.Restart)
		}
	}
	for svcName, dep := range map[string]string{"mongo-import": "mongo-export", "mongodb": "mongo-import"} {
		if c := svc[svcName].DependsOn[dep]["condition"]; c != "service_completed_successfully" {
			t.Errorf("%s depends on %s with %v", svcName, dep, c)
		}
	}
	if c := svc["rocketchat"].DependsOn["mongodb"]["condition"]; c != "service_healthy" {
		t.Errorf("rocketchat depends on mongodb with %v", c)
	}

	// The install path creates and owns only declared volumes.
	declared := map[string]bool{}
	for _, v := range e.Volumes {
		declared[v.Name] = true
	}
	for name, s := range svc {
		for _, m := range s.Volumes {
			src := strings.SplitN(strings.TrimPrefix(m, root), ":", 2)[0]
			if !declared[src] {
				t.Errorf("%s mounts %s, which app.yaml does not declare", name, src)
			}
		}
	}
}

// The one-shot scripts are shell, and a syntax error there only shows up as a
// failed update on a live install.
func TestRocketChat_CopyScriptsAreValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	svc := renderRocketChat(t)
	for _, name := range []string{"mongo-export", "mongo-import"} {
		cmd := svc[name].Command
		if len(cmd) != 1 {
			t.Fatalf("%s command %v, want one script", name, cmd)
		}
		// Compose turns $$ into $ before the shell sees it.
		script := strings.ReplaceAll(cmd[0], "$$", "$")
		if out, err := exec.Command(bash, "-n", "-c", script).CombinedOutput(); err != nil {
			t.Errorf("%s: bash -n: %v\n%s", name, err, out)
		}
		if strings.Contains(strings.ReplaceAll(cmd[0], "$$", ""), "$") {
			t.Errorf("%s has a $ compose would interpolate; write it as $$", name)
		}
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
