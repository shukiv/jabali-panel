package dockerapp

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/tenantcompose"
)

func testHardening() *TenantHardening {
	return &TenantHardening{CgroupParent: "jabali-user-bob.slice", Caps: []string{"CHOWN"}, PIDsLimit: 256}
}

// GH #1903: the expected service set is only a valid oracle if no tenant
// value can change a pinned field. Render every catalog app with every env
// var, a domain, a name and every port set, and require the pinned fields to
// equal the tenant-free render RenderTenant pins. A template that ever makes
// a service, an image or a limit depend on tenant input fails here.
func TestRenderTenant_TenantValuesCannotChangeThePinnedFields(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	if cat.Len() == 0 {
		t.Fatal("catalog loaded zero entries")
	}
	for _, e := range cat.All() {
		overrides := make(map[string]string, len(e.Env))
		for _, v := range e.Env {
			overrides[v.Name] = "marker-" + strings.ToLower(v.Name)
		}
		overrides["SMTP_PORT"] = "465" // take the implicit-TLS branch too
		env, err := MaterialiseEnv(e, overrides)
		if err != nil {
			t.Errorf("%s MaterialiseEnv: %v", e.Slug, err)
			continue
		}
		ports := make(map[string]RuntimePort, len(e.Ports))
		for i, p := range e.Ports {
			ports[p.Name] = RuntimePort{HostPort: 20000 + i, ContainerPort: p.ContainerPort, BindInterface: "127.0.0.1", Protocol: p.Protocol}
		}
		compose, want, err := RenderTenant(e, RenderParams{
			Slug: e.Slug + "-t", Name: "shop", Domain: "shop.example.com",
			ImageChannel: e.ImageChannel, DataRoot: "/var/lib/jabali/docker-apps/" + e.Slug + "-t",
			CPULimit: "1.0", MemoryLimit: "1g", PIDsLimit: 256, Ports: ports, Env: env,
			TenantHardening: testHardening(),
		})
		if err != nil {
			t.Errorf("%s RenderTenant: %v", e.Slug, err)
			continue
		}
		got, err := ServicesFromCompose(compose)
		if err != nil {
			t.Errorf("%s: read the real render: %v", e.Slug, err)
			continue
		}
		if err := want.Check(got); err != nil {
			t.Errorf("%s: a tenant value changes a pinned field: %v", e.Slug, err)
		}
		for name, s := range want {
			if s.PIDs != 256 {
				t.Errorf("%s: service %q pins pids %d, want the hardening's 256", e.Slug, name, s.PIDs)
			}
		}
	}
}

// A template that let an env value out of its scalar would add a service to
// the real render. The expected set is rendered without env values, so it
// does not have that service and the check names it.
func TestRenderTenant_InjectedServiceIsNotExpected(t *testing.T) {
	e := Entry{
		Slug: "leaky",
		composeTmpl: `services:
  app:
    image: example/app:1
    environment:
      FOO: {{ index .Env "FOO" }}
`,
	}
	compose, want, err := RenderTenant(e, RenderParams{
		Slug: "leaky", DataRoot: "/var/lib/jabali/docker-apps/leaky",
		Env:             map[string]string{"FOO": "x\n  miner:\n    image: evil/miner"},
		TenantHardening: testHardening(),
	})
	if err != nil {
		t.Fatalf("RenderTenant: %v", err)
	}
	got, err := ServicesFromCompose(compose)
	if err != nil {
		t.Fatalf("read the real render: %v", err)
	}
	if _, ok := got["miner"]; !ok {
		t.Fatalf("the injection did not reach the real render, so this test proves nothing:\n%s", compose)
	}
	if err := want.Check(got); err == nil || !strings.Contains(err.Error(), `"miner"`) {
		t.Fatalf("expected the injected service to be refused, got %v", err)
	}
}

func TestRenderTenant_RequiresHardening(t *testing.T) {
	e := Entry{Slug: "x", composeTmpl: "services:\n  app:\n    image: a\n"}
	if _, _, err := RenderTenant(e, RenderParams{Slug: "x"}); err == nil {
		t.Fatal("a tenant render without the hardening profile must be refused")
	}
}

func TestServicesFromCompose_ReadsEveryPinnedField(t *testing.T) {
	got, err := ServicesFromCompose(`services:
  app:
    image: example/app:1
    deploy:
      resources:
        limits:
          cpus: "0.5"
          memory: 512m
          pids: 256
  db:
    image: postgres:16
    shm_size: 1gb
    deploy:
      resources:
        limits:
          cpus: 2
          pids: 256
    ulimits:
      nproc: 512
      nofile:
        soft: 1024
        hard: 2048
`)
	if err != nil {
		t.Fatalf("ServicesFromCompose: %v", err)
	}
	want := tenantcompose.Services{
		"app": {Image: "example/app:1", CPUs: 0.5, MemoryBytes: 536870912, PIDs: 256},
		"db": {Image: "postgres:16", CPUs: 2, PIDs: 256, ShmSizeBytes: 1073741824, Ulimits: map[string]tenantcompose.Ulimit{
			"nproc":  {Soft: 512, Hard: 512},
			"nofile": {Soft: 1024, Hard: 2048},
		}},
	}
	if err := want.Check(got); err != nil {
		t.Fatalf("parsed set differs: %v", err)
	}
}

func TestServicesFromCompose_RefusesWhatItCannotPin(t *testing.T) {
	for name, yml := range map[string]string{
		"no image":     "services:\n  app:\n    build: .\n",
		"bad memory":   "services:\n  app:\n    image: a\n    deploy:\n      resources:\n        limits:\n          memory: lots\n",
		"bad shm_size": "services:\n  app:\n    image: a\n    shm_size: huge\n",
		"no services":  "services: {}\n",
	} {
		if _, err := ServicesFromCompose(yml); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// Each tenant-supplied value is stripped from the render the set comes from:
// a template that put name, domain, env or a port into an image would pin the
// bare image, so the real render's image differs and the agent refuses it.
func TestRenderTenant_StripsEveryTenantValue(t *testing.T) {
	e := Entry{
		Slug: "leaky",
		composeTmpl: `services:
  app:
    image: "reg/n{{ .Name }}-d{{ .Domain }}-e{{ index .Env "TAG" }}-p{{ with index .Ports "http" }}{{ .HostPort }}{{ end }}:1"
`,
	}
	_, want, err := RenderTenant(e, RenderParams{
		Slug: "leaky", Name: "shop", Domain: "shop.example.com", DataRoot: "/var/lib/jabali/docker-apps/leaky",
		Env:             map[string]string{"TAG": "evil"},
		Ports:           map[string]RuntimePort{"http": {HostPort: 10001, ContainerPort: 80, BindInterface: "127.0.0.1", Protocol: "tcp"}},
		TenantHardening: testHardening(),
	})
	if err != nil {
		t.Fatalf("RenderTenant: %v", err)
	}
	// A missing port indexes to the zero RuntimePort, so its host port is 0.
	if got := want["app"].Image; got != "reg/n-d-e-p0:1" {
		t.Fatalf("the pinned image carries a tenant value: %q", got)
	}
}
