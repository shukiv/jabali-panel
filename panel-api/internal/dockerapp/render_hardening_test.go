package dockerapp

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// M49 (GH #170): tenant installs get a hardening profile injected into every
// service — cap_drop ALL + the verified allowlist, no-new-privileges, pids cap,
// and cgroup_parent nesting under the tenant's M18 slice.
func TestRender_TenantHardening(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	app, ok := cat.Get("searxng")
	if !ok {
		t.Fatal("searxng missing from catalog")
	}
	out, err := Render(app, RenderParams{
		Slug: "searxng", Name: "search", Domain: "s.example.com",
		ImageChannel: "docker.io/searxng/searxng:2026.6.20-fd42d4fda",
		DataRoot:     "/var/lib/jabali/docker-apps/searxng-u01-search",
		CPULimit:     "1.0", MemoryLimit: "512m",
		Ports: map[string]RuntimePort{"http": {HostPort: 10010, ContainerPort: 8080, BindInterface: "127.0.0.1", Protocol: "tcp"}},
		Env:   map[string]string{"SEARXNG_SECRET": "s3cr3t"},
		TenantHardening: &TenantHardening{
			CgroupParent: "jabali-user-alice.slice",
			Caps:         []string{"CHOWN", "SETUID", "SETGID"},
			PIDsLimit:    200,
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// Parse the result and assert per-service hardening landed.
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("hardened compose not valid YAML: %v\n%s", err, out)
	}
	svcs := doc["services"].(map[string]any)
	if len(svcs) == 0 {
		t.Fatal("no services after hardening")
	}
	for name, sRaw := range svcs {
		s := sRaw.(map[string]any)
		if got := s["security_opt"]; got == nil || !strings.Contains(out, "no-new-privileges:true") {
			t.Errorf("service %q missing no-new-privileges", name)
		}
		if s["cap_drop"] == nil {
			t.Errorf("service %q missing cap_drop", name)
		}
		if s["cap_add"] == nil {
			t.Errorf("service %q missing cap_add allowlist", name)
		}
		if s["cgroup_parent"] != "jabali-user-alice.slice" {
			t.Errorf("service %q cgroup_parent = %v", name, s["cgroup_parent"])
		}
		// pids must live under deploy.resources.limits.pids, NOT the legacy
		// top-level pids_limit key (compose v5 conflict — GH #284).
		if s["pids_limit"] != nil {
			t.Errorf("service %q must not emit legacy top-level pids_limit: %v", name, s["pids_limit"])
		}
		deploy, _ := s["deploy"].(map[string]any)
		res, _ := deploy["resources"].(map[string]any)
		lim, _ := res["limits"].(map[string]any)
		if lim == nil || lim["pids"] != 200 {
			t.Errorf("service %q deploy.resources.limits.pids = %v, want 200", name, lim["pids"])
		}
	}
	// Secret + the env value must survive the re-marshal.
	if !strings.Contains(out, "s3cr3t") {
		t.Errorf("env value lost in re-marshal:\n%s", out)
	}
}

// Admin installs (nil TenantHardening) get the admin baseline, not the tenant
// profile: no-new-privileges, NET_RAW and MKNOD dropped, the PIDs limit, and
// no cap_drop ALL or tenant slice.
func TestRender_AdminBaselineWhenNoTenantHardening(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	app, _ := cat.Get("searxng")
	out, err := Render(app, RenderParams{
		Slug: "searxng", Name: "s", Domain: "s.example.com",
		ImageChannel: "docker.io/searxng/searxng:x", DataRoot: "/d",
		CPULimit: "1.0", MemoryLimit: "512m", PIDsLimit: 300,
		Ports: map[string]RuntimePort{"http": {HostPort: 10010, ContainerPort: 8080, BindInterface: "127.0.0.1", Protocol: "tcp"}},
		Env:   map[string]string{"SEARXNG_SECRET": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "cgroup_parent") || strings.Contains(out, "- ALL") {
		t.Errorf("admin render must not carry the tenant profile:\n%s", out)
	}
	var doc struct {
		Services map[string]struct {
			SecurityOpt []string `yaml:"security_opt"`
			CapDrop     []string `yaml:"cap_drop"`
			PidsLimit   *int     `yaml:"pids_limit"`
			Deploy      struct {
				Resources struct {
					Limits struct {
						Pids int `yaml:"pids"`
					} `yaml:"limits"`
				} `yaml:"resources"`
			} `yaml:"deploy"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	for name, s := range doc.Services {
		if !slices.Contains(s.SecurityOpt, "no-new-privileges:true") {
			t.Errorf("%s: no no-new-privileges: %v", name, s.SecurityOpt)
		}
		if !slices.Contains(s.CapDrop, "NET_RAW") || !slices.Contains(s.CapDrop, "MKNOD") {
			t.Errorf("%s: cap_drop %v, want NET_RAW and MKNOD", name, s.CapDrop)
		}
		if s.Deploy.Resources.Limits.Pids != 300 || s.PidsLimit != nil {
			t.Errorf("%s: pids %d (legacy pids_limit %v), want deploy limit 300 only",
				name, s.Deploy.Resources.Limits.Pids, s.PidsLimit)
		}
	}
}

// The admin baseline only adds: every catalog app's rendered compose is the
// template output plus the baseline keys, with nothing else changed. A
// template's own security_opt / cap_drop entries survive, and a cap the
// template adds back explicitly is not dropped.
func TestRender_AdminBaselineOnlyAddsKeys(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	for _, e := range cat.All() {
		env, err := MaterialiseEnv(e, nil)
		if err != nil {
			t.Fatalf("%s MaterialiseEnv: %v", e.Slug, err)
		}
		ports := make(map[string]RuntimePort, len(e.Ports))
		for i, p := range e.Ports {
			ports[p.Name] = RuntimePort{HostPort: 20000 + i, ContainerPort: p.ContainerPort,
				BindInterface: "127.0.0.1", Protocol: p.Protocol}
		}
		params := RenderParams{
			Slug: e.Slug, Name: "t", Domain: e.Slug + ".example.com",
			ImageChannel: e.ImageChannel, DataRoot: "/var/lib/jabali/docker-apps/" + e.Slug,
			CPULimit: "1.0", MemoryLimit: "1g", PIDsLimit: e.Resources.PIDs, Ports: ports, Env: env,
		}
		raw, err := renderTemplate(e, params)
		if err != nil {
			t.Fatalf("%s renderTemplate: %v", e.Slug, err)
		}
		hardened, err := Render(e, params)
		if err != nil {
			t.Fatalf("%s Render: %v", e.Slug, err)
		}
		var before, after map[string]any
		if err := yaml.Unmarshal([]byte(raw), &before); err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal([]byte(hardened), &after); err != nil {
			t.Fatalf("%s: hardened compose is not valid YAML: %v", e.Slug, err)
		}
		bs, _ := before["services"].(map[string]any)
		as, _ := after["services"].(map[string]any)
		for name, sv := range as {
			s := sv.(map[string]any)
			orig, _ := bs[name].(map[string]any)
			for _, keep := range stringsOf(orig["security_opt"]) {
				if !slices.Contains(stringsOf(s["security_opt"]), keep) {
					t.Errorf("%s/%s: template security_opt %q lost", e.Slug, name, keep)
				}
			}
			for _, keep := range stringsOf(orig["cap_drop"]) {
				if !slices.Contains(stringsOf(s["cap_drop"]), keep) {
					t.Errorf("%s/%s: template cap_drop %q lost", e.Slug, name, keep)
				}
			}
			for _, c := range stringsOf(orig["cap_add"]) {
				if slices.Contains(stringsOf(s["cap_drop"]), c) {
					t.Errorf("%s/%s: drops %q, which the template adds back", e.Slug, name, c)
				}
			}
			if !slices.Contains(stringsOf(s["security_opt"]), "no-new-privileges:true") {
				t.Errorf("%s/%s: no no-new-privileges", e.Slug, name)
			}
			// Remove what the baseline adds; the rest must be unchanged.
			for _, m := range []map[string]any{s, orig} {
				delete(m, "security_opt")
				delete(m, "cap_drop")
				delete(m, "pids_limit")
				if d, ok := m["deploy"].(map[string]any); ok {
					if r, ok := d["resources"].(map[string]any); ok {
						if l, ok := r["limits"].(map[string]any); ok {
							delete(l, "pids")
						}
					}
				}
			}
		}
		pruneEmpty(after)
		pruneEmpty(before)
		if !reflect.DeepEqual(before, after) {
			t.Errorf("%s: the admin baseline changed more than its own keys\nbefore: %v\nafter:  %v",
				e.Slug, before, after)
		}
	}
}

func stringsOf(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// pruneEmpty drops mappings left empty once the baseline's keys are removed
// (a deploy.resources.limits the baseline created only to hold pids).
func pruneEmpty(m map[string]any) bool {
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok && pruneEmpty(sub) {
			delete(m, k)
		}
	}
	return len(m) == 0
}
