package filesops

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"
)

// TestWireShape guards against JSON-tag drift on the panel↔agent boundary. The
// panel-agent files.* commands parse their params from these exact JSON keys; a
// rename here (or there) produces silent runtime validation failures that unit
// tests with mock agents do NOT catch.
//
// If this test fails, change it AND the matching struct in
// panel-agent/internal/commands/files_*.go together — never one without the
// other. Payloads are built through the exported builders so the same test also
// proves each builder stamps the scope and its verb fields onto the right keys.
func TestWireShape(t *testing.T) {
	s := Scope{UserID: "u", Username: "shuki"}
	cases := []struct {
		name    string
		payload any
		want    []string
	}{
		{"files.list", List(s, "/home/shuki"), []string{"path", "user_id", "username"}},
		{"files.read", Read(s, "/home/shuki/file.txt", 1000), []string{"limit", "path", "user_id", "username"}},
		{"files.read_no_limit", Read(s, "/home/shuki/file.txt", 0), []string{"path", "user_id", "username"}},
		{"files.stat", Stat(s, "/home/shuki/file.txt"), []string{"path", "user_id", "username"}},
		{"files.du", Du(s, "/home/shuki"), []string{"path", "user_id", "username"}},
		{"files.mkdir", Mkdir(s, "/home/shuki/newdir"), []string{"path", "user_id", "username"}},
		{"files.write", Write(s, "/home/shuki/file.txt", "hello"), []string{"content", "path", "user_id", "username"}},
		{"files.delete", Delete(s, "/home/shuki/file.txt", false), []string{"path", "user_id", "username"}},
		{"files.delete_recursive", Delete(s, "/home/shuki/dir", true), []string{"path", "recursive", "user_id", "username"}},
		{"files.rename", Rename(s, "/home/shuki/old.txt", "/home/shuki/new.txt"), []string{"new_path", "old_path", "user_id", "username"}},
		{"files.move", Move(s, "/home/shuki/dir-a/thing.txt", "/home/shuki/dir-b/thing.txt"), []string{"new_path", "old_path", "user_id", "username"}},
		{"files.copy", Copy(s, "/home/shuki/a.txt", "/home/shuki/dir/a.txt"), []string{"dst_path", "src_path", "user_id", "username"}},
		{"files.chmod", Chmod(s, "/home/shuki/file.txt", "0644"), []string{"mode", "path", "user_id", "username"}},
		{"files.archive", Archive(s, []string{"/home/shuki/a.txt", "/home/shuki/b"}), []string{"paths", "user_id", "username"}},
		{"files.extract", Extract(s, "/home/shuki/a.tar.gz", ""), []string{"path", "user_id", "username"}},
		{"files.extract_with_dest", Extract(s, "/home/shuki/a.tar.gz", "/home/shuki/out"), []string{"dest", "path", "user_id", "username"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := make([]string, 0, len(m))
			for k := range m {
				got = append(got, k)
			}
			sort.Strings(got)
			if len(got) != len(tc.want) {
				t.Fatalf("wrong key count: got %v want %v", got, tc.want)
			}
			for i, k := range got {
				if k != tc.want[i] {
					t.Fatalf("key[%d]: got %q want %q (full got=%v want=%v)", i, k, tc.want[i], got, tc.want)
				}
			}
		})
	}
}

// TestAdminRootOnWire pins that AdminRoot only appears on the wire when true —
// the REST admin File Manager sets it, the CLI never does (its old inline maps
// omitted the key entirely), so the shared struct must stay byte-identical to
// the CLI's historical wire for a tenant-scoped call.
func TestAdminRootOnWire(t *testing.T) {
	tenant, err := json.Marshal(List(Scope{UserID: "u", Username: "shuki"}, "/home/shuki"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(tenant); got != `{"user_id":"u","username":"shuki","path":"/home/shuki"}` {
		t.Fatalf("tenant list wire: %s", got)
	}
	admin, err := json.Marshal(List(Scope{UserID: "u", Username: "admin", AdminRoot: true}, "/etc"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(admin, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["admin_root"].(bool); !ok || !v {
		t.Fatalf("admin_root must be true on the wire for an admin-root scope: %s", admin)
	}
}

func TestValidateNewName(t *testing.T) {
	ok := []string{"file.txt", "new-name", ".hidden", "a.b.c"}
	for _, n := range ok {
		if err := ValidateNewName(n); err != nil {
			t.Errorf("ValidateNewName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"a/b", `a\b`, ".", "..", "dir/../etc"}
	for _, n := range bad {
		if err := ValidateNewName(n); !errors.Is(err, ErrNewNameNotBare) {
			t.Errorf("ValidateNewName(%q) = %v, want ErrNewNameNotBare", n, err)
		}
	}
}

// TestRenameTargetRejectsTraversal pins the reconciliation: a rename target of
// ".." (or a backslash name) is now rejected pre-flight on BOTH adapters. The
// CLI previously only rejected a forward slash, so `rename foo/bar ..` derived
// "foo" and reached the agent; the shared rule stops it at the panel edge.
func TestRenameTargetRejectsTraversal(t *testing.T) {
	if _, err := RenameTarget("/home/shuki/dir/file", ".."); !errors.Is(err, ErrNewNameNotBare) {
		t.Fatalf(`RenameTarget(.., "..") = %v, want ErrNewNameNotBare`, err)
	}
	got, err := RenameTarget("/home/shuki/dir/old.txt", "new.txt")
	if err != nil {
		t.Fatalf("RenameTarget valid: %v", err)
	}
	if got != "/home/shuki/dir/new.txt" {
		t.Fatalf("RenameTarget derived %q, want /home/shuki/dir/new.txt", got)
	}
}

func TestMoveAndCopyTarget(t *testing.T) {
	for _, tc := range []struct {
		destDir, path, want string
	}{
		{"/home/shuki/dst", "/home/shuki/src/thing.txt", "/home/shuki/dst/thing.txt"},
		{"/home/shuki/dst", "/home/shuki/src/dir", "/home/shuki/dst/dir"},
	} {
		got, err := MoveTarget(tc.path, tc.destDir)
		if err != nil {
			t.Fatalf("MoveTarget(%q,%q): %v", tc.path, tc.destDir, err)
		}
		if got != tc.want {
			t.Fatalf("MoveTarget = %q, want %q", got, tc.want)
		}
		got, err = CopyTarget(tc.path, tc.destDir)
		if err != nil {
			t.Fatalf("CopyTarget(%q,%q): %v", tc.path, tc.destDir, err)
		}
		if got != tc.want {
			t.Fatalf("CopyTarget = %q, want %q", got, tc.want)
		}
	}
	if _, err := MoveTarget("/home/shuki/x", "/home/shuki/../etc"); !errors.Is(err, ErrDestDirTraversal) {
		t.Fatalf("MoveTarget traversal = %v, want ErrDestDirTraversal", err)
	}
	if _, err := CopyTarget("/home/shuki/x", "../etc"); !errors.Is(err, ErrDestDirTraversal) {
		t.Fatalf("CopyTarget traversal = %v, want ErrDestDirTraversal", err)
	}
}

func TestValidateArchivePaths(t *testing.T) {
	if err := ValidateArchivePaths(nil); !errors.Is(err, ErrNoPaths) {
		t.Fatalf("ValidateArchivePaths(nil) = %v, want ErrNoPaths", err)
	}
	if err := ValidateArchivePaths([]string{"/home/shuki/a"}); err != nil {
		t.Fatalf("ValidateArchivePaths(one) = %v, want nil", err)
	}
}
