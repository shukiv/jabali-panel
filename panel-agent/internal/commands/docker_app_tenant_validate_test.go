package commands

import (
	"os"
	"path/filepath"
	"testing"
)

const root = "/var/lib/jabali/docker-apps/memos-u01-notes"

// cfg wraps service JSON in a resolved config whose project name is root's
// base name, as `docker compose config` prints it for a compose that sets
// none (GH #1903).
func cfg(svc string) []byte {
	return []byte(`{"name":"memos-u01-notes","services":{` + svc + `}}`)
}

func TestValidateTenantCompose_OK(t *testing.T) {
	j := cfg(`"memos":{"cap_add":["CHOWN","SETUID"],"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"],"cgroup_parent":"jabali-user-test.slice","volumes":[{"type":"bind","source":"` + root + `/data","target":"/data"}]}`)
	if err := validateTenantCompose(j, []string{"CHOWN", "SETUID", "SETGID"}, root, ""); err != nil {
		t.Fatalf("expected clean, got %v", err)
	}
}

func TestValidateTenantCompose_RejectsPrivileged(t *testing.T) {
	j := cfg(`"memos":{"privileged":true}`)
	if err := validateTenantCompose(j, nil, root, ""); err == nil {
		t.Fatal("privileged service must be rejected")
	}
}

func TestValidateTenantCompose_RejectsForeignCap(t *testing.T) {
	j := cfg(`"memos":{"cap_add":["NET_ADMIN"]}`)
	if err := validateTenantCompose(j, []string{"CHOWN"}, root, ""); err == nil {
		t.Fatal("cap outside allowlist must be rejected")
	}
}

func TestValidateTenantCompose_CapPrefixNormalised(t *testing.T) {
	// "CAP_CHOWN" in config vs "chown" in allowlist must still match.
	j := cfg(`"memos":{"cap_add":["CAP_CHOWN"],"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"],"cgroup_parent":"jabali-user-test.slice","target":"/x"}`)
	if err := validateTenantCompose(j, []string{"chown"}, root, ""); err != nil {
		t.Fatalf("normalised cap should match: %v", err)
	}
}

func TestValidateTenantCompose_RejectsHostBindMount(t *testing.T) {
	j := cfg(`"memos":{"volumes":[{"type":"bind","source":"/var/run/docker.sock","target":"/sock"}]}`)
	if err := validateTenantCompose(j, nil, root, ""); err == nil {
		t.Fatal("host bind-mount outside data tree must be rejected")
	}
}

func TestValidateTenantCompose_RejectsParentEscape(t *testing.T) {
	// A source that prefix-matches the root string but escapes it.
	j := cfg(`"memos":{"volumes":[{"type":"bind","source":"` + root + `-evil/data","target":"/data"}]}`)
	if err := validateTenantCompose(j, nil, root, ""); err == nil {
		t.Fatal("sibling-dir escape (root-evil) must be rejected")
	}
}

func TestValidateTenantCompose_NoServices(t *testing.T) {
	if err := validateTenantCompose([]byte(`{"services":{}}`), nil, root, ""); err == nil {
		t.Fatal("empty services must error")
	}
}

// Host-namespace / device escapes (GH #450).
func TestValidateTenantCompose_RejectsHostEscapes(t *testing.T) {
	cases := map[string]string{
		"network_mode_host":     `"memos":{"network_mode":"host"}`,
		"pid_host":              `"memos":{"pid":"host"}`,
		"ipc_host":              `"memos":{"ipc":"host"}`,
		"uts_host":              `"memos":{"uts":"host"}`,
		"userns_host":           `"memos":{"userns_mode":"host"}`,
		"network_container":     `"memos":{"network_mode":"container:other"}`,
		"pid_container":         `"memos":{"pid":"container:other"}`,
		"devices":               `"memos":{"devices":["/dev/sda:/dev/sda"]}`,
		"device_cgroup_rules":   `"memos":{"device_cgroup_rules":["c 1:3 rmw"]}`,
		"volumes_from":          `"memos":{"volumes_from":["other"]}`,
		"security_opt_seccomp":  `"memos":{"security_opt":["seccomp:unconfined"]}`,
		"security_opt_apparmor": `"memos":{"security_opt":["apparmor:unconfined"]}`,
	}
	for name, svc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateTenantCompose(cfg(svc), []string{"CHOWN"}, root, ""); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

func TestValidateTenantCompose_AllowsInjectedSecurityOpt(t *testing.T) {
	// The panel injects no-new-privileges:true — must pass (both : and = forms).
	for _, so := range []string{"no-new-privileges:true", "no-new-privileges=true"} {
		j := cfg(`"memos":{"cap_drop":["ALL"],"cgroup_parent":"jabali-user-test.slice","security_opt":["` + so + `"]}`)
		if err := validateTenantCompose(j, []string{"CHOWN"}, root, ""); err != nil {
			t.Fatalf("injected security_opt %q should pass: %v", so, err)
		}
	}
}

func TestValidateTenantCompose_AllowsBenignNamespaceModes(t *testing.T) {
	// none/private/service refs are not host escapes.
	j := cfg(`"memos":{"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"],"cgroup_parent":"jabali-user-test.slice","network_mode":"none","ipc":"private"}`)
	if err := validateTenantCompose(j, []string{"CHOWN"}, root, ""); err != nil {
		t.Fatalf("benign namespace modes should pass: %v", err)
	}
}

func TestValidateTenantCompose_RejectsPublicPort(t *testing.T) {
	j := cfg(`"web":{"ports":[{"published":"8080","host_ip":"0.0.0.0","target":80,"protocol":"tcp"}]}`)
	if err := validateTenantCompose(j, nil, root, ""); err == nil {
		t.Error("public 0.0.0.0 published port must be rejected for tenant installs")
	}
	// empty host_ip = docker binds 0.0.0.0 → also rejected
	j2 := cfg(`"web":{"ports":[{"published":"8080","host_ip":"","target":80,"protocol":"tcp"}]}`)
	if err := validateTenantCompose(j2, nil, root, ""); err == nil {
		t.Error("empty host_ip (defaults to 0.0.0.0) must be rejected")
	}
}

func TestValidateTenantCompose_AllowsLoopbackPort(t *testing.T) {
	j := cfg(`"web":{"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"],"cgroup_parent":"jabali-user-test.slice","ports":[{"published":"10001","host_ip":"127.0.0.1","target":80,"protocol":"tcp"}]}`)
	if err := validateTenantCompose(j, nil, root, ""); err != nil {
		t.Errorf("loopback-published port must be allowed: %v", err)
	}
}

func TestValidateTenantCompose_RejectsNamedVolume(t *testing.T) {
	j := cfg(`"memos":{"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"],"cgroup_parent":"jabali-user-test.slice","volumes":[{"type":"volume","source":"vol","target":"/x"}]}`)
	if err := validateTenantCompose(j, []string{"CHOWN"}, root, ""); err == nil {
		t.Error("named volume must be rejected for tenant installs (#514)")
	}
}

func TestValidateTenantCompose_RequiresHardening(t *testing.T) {
	// no cap_drop ALL
	if err := validateTenantCompose(cfg(`"memos":{"security_opt":["no-new-privileges:true"]}`), nil, root, ""); err == nil {
		t.Error("missing cap_drop: ALL must be rejected (#516)")
	}
	// no no-new-privileges
	if err := validateTenantCompose(cfg(`"memos":{"cap_drop":["ALL"]}`), nil, root, ""); err == nil {
		t.Error("missing no-new-privileges:true must be rejected (#516)")
	}
}

func TestValidateTenantCompose_RejectsHostFileInputs(t *testing.T) {
	hard := `"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"],"cgroup_parent":"jabali-user-test.slice",`
	for _, field := range []string{
		`"build":"."`,
		`"env_file":[".env"]`,
		`"secrets":["s1"]`,
		`"configs":["c1"]`,
	} {
		j := cfg(`"memos":{` + hard + field + `}`)
		if err := validateTenantCompose(j, nil, root, ""); err == nil {
			t.Errorf("host-file input %s must be rejected (#518)", field)
		}
	}
	// top-level secrets
	j := []byte(`{"name":"memos-u01-notes","services":{"memos":{` + hard + `}},"secrets":{"s1":{"file":"/etc/x"}}}`)
	if err := validateTenantCompose(j, nil, root, ""); err == nil {
		t.Error("top-level secrets must be rejected (#518)")
	}
}

func TestValidateTenantCompose_RequiresTenantCgroupParent(t *testing.T) {
	base := `"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"]`
	// missing cgroup_parent
	if err := validateTenantCompose(cfg(`"memos":{`+base+`}`), nil, root, ""); err == nil {
		t.Error("missing cgroup_parent must be rejected (#516)")
	}
	// wrong cgroup_parent (not a tenant slice)
	if err := validateTenantCompose(cfg(`"memos":{`+base+`,"cgroup_parent":"system.slice"}`), nil, root, ""); err == nil {
		t.Error("non-tenant cgroup_parent must be rejected (#516)")
	}
	// correct tenant slice passes
	if err := validateTenantCompose(cfg(`"memos":{`+base+`,"cgroup_parent":"jabali-user-bob.slice"}`), nil, root, ""); err != nil {
		t.Errorf("valid tenant cgroup_parent must pass: %v", err)
	}
}

// Gitea #525: when the panel passes the expected owner slice, a compose that
// declares a DIFFERENT tenant slice must be rejected (cross-tenant spoof).
func TestValidateTenantCompose_ExactOwnerCgroup(t *testing.T) {
	base := `"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"]`
	owner := "jabali-user-bob.slice"
	// another tenant's slice — matches the generic pattern but not the owner
	if err := validateTenantCompose(cfg(`"memos":{`+base+`,"cgroup_parent":"jabali-user-eve.slice"}`), nil, root, owner); err == nil {
		t.Error("a different tenant slice must be rejected when owner slice is enforced (#525)")
	}
	// exact owner slice passes
	if err := validateTenantCompose(cfg(`"memos":{`+base+`,"cgroup_parent":"jabali-user-bob.slice"}`), nil, root, owner); err != nil {
		t.Errorf("exact owner slice must pass: %v", err)
	}
}

// Gitea #531: a bind source that is a symlink resolving outside the data tree
// must be rejected even though its lexical path is under the data root.
func TestValidateTenantCompose_SymlinkBindEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memos-u01-notes")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// /root/evil -> /etc  (symlink under the data root pointing outside it)
	link := filepath.Join(root, "evil")
	if err := os.Symlink("/etc", link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	base := `"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"],"cgroup_parent":"jabali-user-bob.slice"`
	svc := `"memos":{` + base + `,"volumes":[{"type":"bind","source":"` + link + `/passwd","target":"/x"}]}`
	if err := validateTenantCompose(cfg(svc), nil, root, ""); err == nil {
		t.Error("bind via a symlink escaping the data tree must be rejected (#531)")
	}
	// a genuine path under root passes
	good := `"memos":{` + base + `,"volumes":[{"type":"bind","source":"` + filepath.Join(root, "data") + `","target":"/x"}]}`
	if err := validateTenantCompose(cfg(good), nil, root, ""); err != nil {
		t.Errorf("bind under the data tree must pass: %v", err)
	}
}
