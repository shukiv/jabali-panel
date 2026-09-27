package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/tenantcompose"
)

// probedConfig is a hardened two-service tenant app as `docker compose config
// --format json` (Compose v5.2.0) printed it on a test host: byte sizes as
// decimal strings, cpus as a number, a single-value ulimit as a bare number.
const probedConfig = `{
  "name": "memos-u01-notes",
  "networks": {"default": {"name": "memos-u01-notes_default", "ipam": {}}},
  "services": {
    "db": {
      "cap_drop": ["ALL"], "cgroup_parent": "jabali-user-bob.slice", "command": null,
      "deploy": {"resources": {"limits": {"pids": 256}}, "placement": {}},
      "entrypoint": null, "image": "postgres:16", "networks": {"default": null},
      "security_opt": ["no-new-privileges:true"], "shm_size": "1073741824",
      "ulimits": {"nofile": {"soft": 1024, "hard": 2048}, "nproc": 512}
    },
    "web": {
      "cap_drop": ["ALL"], "cgroup_parent": "jabali-user-bob.slice", "command": null,
      "deploy": {"resources": {"limits": {"cpus": 0.5, "memory": "536870912", "pids": 256}}, "placement": {}},
      "entrypoint": null, "image": "nginx:alpine@sha256:0000000000000000000000000000000000000000000000000000000000000000",
      "networks": {"default": null},
      "ports": [{"mode": "ingress", "host_ip": "127.0.0.1", "target": 80, "published": "18080", "protocol": "tcp"}],
      "security_opt": ["no-new-privileges:true"]
    }
  }
}`

const pinOwner = "jabali-user-bob.slice"

func probedServices() tenantcompose.Services {
	return tenantcompose.Services{
		"web": {Image: "nginx:alpine@sha256:0000000000000000000000000000000000000000000000000000000000000000", CPUs: 0.5, MemoryBytes: 536870912, PIDs: 256},
		"db": {Image: "postgres:16", PIDs: 256, ShmSizeBytes: 1073741824, Ulimits: map[string]tenantcompose.Ulimit{
			"nofile": {Soft: 1024, Hard: 2048},
			"nproc":  {Soft: 512, Hard: 512},
		}},
	}
}

// probed returns probedConfig after mutate edits its decoded form.
func probed(t *testing.T, mutate func(doc map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(probedConfig), &doc); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(doc)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func svcOf(doc map[string]any, name string) map[string]any {
	return doc["services"].(map[string]any)[name].(map[string]any)
}

func TestTenantPin_ProbedConfigPasses(t *testing.T) {
	j := probed(t, nil)
	if err := validateTenantCompose(j, nil, root, pinOwner); err != nil {
		t.Fatalf("the probed hardened config must pass: %v", err)
	}
	if err := validateTenantServices(j, probedServices()); err != nil {
		t.Fatalf("the probed config must match its rendered service set: %v", err)
	}
}

// GH #1903 tier A: rules every tenant bring-up enforces, with no expected set.
func TestTenantPin_RejectsProjectAndNetworkEscapes(t *testing.T) {
	cases := map[string]struct {
		mutate func(doc map[string]any)
		want   string
	}{
		"project renamed": {func(d map[string]any) { d["name"] = "otherproj" }, "project name"},
		"external default network": {func(d map[string]any) {
			d["networks"] = map[string]any{"default": map[string]any{"name": "otherapp_default", "ipam": map[string]any{}, "external": true}}
		}, "external"},
		"default network renamed": {func(d map[string]any) {
			d["networks"] = map[string]any{"default": map[string]any{"name": "x_default", "ipam": map[string]any{}}}
		}, "named"},
		"extra network": {func(d map[string]any) {
			d["networks"].(map[string]any)["evil2"] = map[string]any{"name": "x_default", "ipam": map[string]any{}, "external": true}
		}, `network "evil2"`},
		"driver_opts": {func(d map[string]any) {
			d["networks"].(map[string]any)["default"].(map[string]any)["driver_opts"] = map[string]any{"com.docker.network.bridge.name": "br-x"}
		}, "driver_opts"},
		"own subnet": {func(d map[string]any) {
			d["networks"].(map[string]any)["default"].(map[string]any)["ipam"] = map[string]any{"config": []any{map[string]any{"subnet": "10.99.0.0/24"}}}
		}, "IPAM"},
		"host driver": {func(d map[string]any) {
			d["networks"].(map[string]any)["default"].(map[string]any)["driver"] = "host"
		}, "driver"},
		"service joins another network": {func(d map[string]any) {
			svcOf(d, "web")["networks"] = map[string]any{"default": nil, "evil2": nil}
		}, `joins network "evil2"`},
		"network_mode bridge":          {func(d map[string]any) { svcOf(d, "web")["network_mode"] = "bridge" }, "network_mode"},
		"network_mode missing service": {func(d map[string]any) { svcOf(d, "web")["network_mode"] = "service:ghost" }, "network_mode"},
		"cgroup namespace host":        {func(d map[string]any) { svcOf(d, "web")["cgroup"] = "host" }, "cgroup"},
	}
	for name, tc := range cases {
		err := validateTenantCompose(probed(t, tc.mutate), nil, root, pinOwner)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

func TestTenantPin_AllowsNetworkModeOfOwnService(t *testing.T) {
	j := probed(t, func(d map[string]any) {
		db := svcOf(d, "db")
		delete(db, "networks")
		db["network_mode"] = "service:web"
	})
	if err := validateTenantCompose(j, nil, root, pinOwner); err != nil {
		t.Fatalf("sharing a sibling service's network must pass: %v", err)
	}
}

// Shapes taken from `docker compose config` on the test host.
func TestTenantPin_RejectsKnobsTheRenderNeverEmits(t *testing.T) {
	for key, val := range map[string]any{
		"gpus":             []any{map[string]any{"count": -1}},
		"runtime":          "runc",
		"oom_score_adj":    -500,
		"oom_kill_disable": true,
		"sysctls":          map[string]any{"net.core.somaxconn": "1024"},
		"mem_limit":        "314572800",
		"memswap_limit":    "1073741824",
		"cpus":             0.25,
		"cpu_shares":       512,
		"cpu_rt_runtime":   1000,
		"cpuset":           "0",
	} {
		j := probed(t, func(d map[string]any) { svcOf(d, "web")[key] = val })
		err := validateTenantCompose(j, nil, root, pinOwner)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: got %v, want it refused", key, err)
		}
	}
	j := probed(t, func(d map[string]any) {
		svcOf(d, "web")["deploy"].(map[string]any)["resources"].(map[string]any)["reservations"] =
			map[string]any{"devices": []any{map[string]any{"capabilities": []any{"gpu"}, "count": -1}}}
	})
	if err := validateTenantCompose(j, nil, root, pinOwner); err == nil || !strings.Contains(err.Error(), "reservations") {
		t.Errorf("a device reservation must be refused, got %v", err)
	}
}

// The M49 hardening wrote the legacy pids_limit before GH #284, so an on-disk
// compose may still carry it: tier A lets it through, the fresh-render pin
// does not.
func TestTenantPin_LegacyPidsLimit(t *testing.T) {
	j := probed(t, func(d map[string]any) { svcOf(d, "db")["pids_limit"] = 256 })
	if err := validateTenantCompose(j, nil, root, pinOwner); err != nil {
		t.Fatalf("an older on-disk compose with pids_limit must still start: %v", err)
	}
	if err := validateTenantServices(j, probedServices()); err == nil || !strings.Contains(err.Error(), "pids_limit") {
		t.Fatalf("a fresh render carrying pids_limit must be refused, got %v", err)
	}
}

// GH #1903 tier B: the resolved services must be exactly the rendered set.
func TestTenantPin_ServiceSetDrift(t *testing.T) {
	cases := map[string]struct {
		mutate func(doc map[string]any)
		want   string
	}{
		"extra service": {func(d map[string]any) {
			d["services"].(map[string]any)["miner"] = map[string]any{"image": "evil/miner"}
		}, `"miner"`},
		"service removed": {func(d map[string]any) { delete(d["services"].(map[string]any), "db") }, `"db"`},
		"image swapped":   {func(d map[string]any) { svcOf(d, "web")["image"] = "evil/web:latest" }, "image"},
		"memory raised": {func(d map[string]any) {
			svcOf(d, "web")["deploy"].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any)["memory"] = "8589934592"
		}, "memory"},
		"cpus raised": {func(d map[string]any) {
			svcOf(d, "web")["deploy"].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any)["cpus"] = 4
		}, "cpus"},
		"pids removed": {func(d map[string]any) {
			delete(svcOf(d, "db")["deploy"].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any), "pids")
		}, "pids"},
		"unknown limit": {func(d map[string]any) {
			svcOf(d, "web")["deploy"].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any)["blkio"] = 1
		}, "limits.blkio"},
		"shm raised": {func(d map[string]any) { svcOf(d, "db")["shm_size"] = "8589934592" }, "shm_size"},
		"ulimit raised": {func(d map[string]any) {
			svcOf(d, "db")["ulimits"] = map[string]any{"nofile": map[string]any{"soft": 1024, "hard": 1048576}, "nproc": 512}
		}, "ulimit nofile"},
		"ulimit added": {func(d map[string]any) { svcOf(d, "web")["ulimits"] = map[string]any{"memlock": -1} }, "ulimit memlock"},
	}
	for name, tc := range cases {
		err := validateTenantServices(probed(t, tc.mutate), probedServices())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

func agentErrMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A tenant compose write without the expected set is refused before anything
// touches disk; a recovery dispatch, which reuses the on-disk compose, is not.
func TestTenantPin_InstallRequiresTheServiceSet(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"slug": "memos-u01-notes", "compose_yml": "services: {}\n", "tenant_validate": true,
		"tenant_cgroup": pinOwner,
	})
	_, err := dockerAppInstallHandler(context.Background(), params)
	if !strings.Contains(agentErrMessage(err), "no expected service set") {
		t.Fatalf("install without tenant_services must be refused, got %v", err)
	}
	recovery, _ := json.Marshal(map[string]any{
		"slug": "memos-u01-notes-none", "compose_yml": dockerAppComposeRecovery, "tenant_validate": true,
	})
	_, err = dockerAppInstallHandler(context.Background(), recovery)
	if strings.Contains(agentErrMessage(err), "no expected service set") {
		t.Fatalf("a recovery dispatch reuses the on-disk compose and must not need the set: %v", err)
	}
}

func TestTenantPin_UpdateRequiresTheServiceSetOnlyWhenItWritesCompose(t *testing.T) {
	withCompose, _ := json.Marshal(map[string]any{
		"slug": "memos-u01-notes", "compose_yml": "services: {}\n", "tenant_validate": true,
	})
	_, err := dockerAppUpdateHandler(context.Background(), withCompose)
	if !strings.Contains(agentErrMessage(err), "no expected service set") {
		t.Fatalf("a re-rendering update without tenant_services must be refused, got %v", err)
	}
	onDisk, _ := json.Marshal(map[string]any{"slug": "memos-u01-notes-none", "tenant_validate": true})
	_, err = dockerAppUpdateHandler(context.Background(), onDisk)
	if strings.Contains(agentErrMessage(err), "no expected service set") {
		t.Fatalf("an image-only update keeps the on-disk compose and must not need the set: %v", err)
	}
}

// runTenantComposeValidation applies the service-set pin when a door passes
// one, and only the tier A rules when it passes nil.
func TestTenantPin_RunValidationAppliesThePassedSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memos-u01-notes")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(fixture, []byte(probedConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "cat", fixture)
	}
	t.Cleanup(func() { execCommandContext = prev })

	ctx := context.Background()
	if err := runTenantComposeValidation(ctx, dir, nil, pinOwner, probedServices()); err != nil {
		t.Fatalf("matching set must pass: %v", err)
	}
	other := probedServices()
	web := other["web"]
	web.Image = "nginx:alpine"
	other["web"] = web
	if err := runTenantComposeValidation(ctx, dir, nil, pinOwner, other); err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("a set that does not match must be refused, got %v", err)
	}
	if err := runTenantComposeValidation(ctx, dir, nil, pinOwner, nil); err != nil {
		t.Fatalf("with no set only the tier A rules apply: %v", err)
	}
}
