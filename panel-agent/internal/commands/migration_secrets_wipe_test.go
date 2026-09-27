package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// JAB-357: migration.secrets_wipe removes a job's source credentials when the
// panel cancels or destroys the job; the panel itself cannot unlink in the
// root:jabali 0750 secrets directory. It removes only <job-id>.env, a regular
// file, and treats a missing file as done.
func TestRemoveMigrationSecret(t *testing.T) {
	const id = "01KZ0000000000000000000000"

	t.Run("removes the job's env file and nothing else", func(t *testing.T) {
		dir := t.TempDir()
		env := filepath.Join(dir, id+".env")
		pin := filepath.Join(dir, id+".known_hosts")
		other := filepath.Join(dir, "01KZ1111111111111111111111.env")
		for _, p := range []string{env, pin, other} {
			if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
				t.Fatal(err)
			}
		}
		removed, err := removeMigrationSecret(dir, id)
		if err != nil || !removed {
			t.Fatalf("removed=%v err=%v, want true nil", removed, err)
		}
		if _, err := os.Stat(env); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the env file must be gone: %v", err)
		}
		for _, p := range []string{pin, other} {
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("%s must be kept: %v", p, err)
			}
		}
	})

	t.Run("a missing file is done", func(t *testing.T) {
		removed, err := removeMigrationSecret(t.TempDir(), id)
		if err != nil || removed {
			t.Fatalf("removed=%v err=%v, want false nil", removed, err)
		}
	})

	t.Run("a directory in its place is refused and kept", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, id+".env")
		if err := os.Mkdir(p, 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := removeMigrationSecret(dir, id); err == nil {
			t.Fatal("a non-regular file must be refused")
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("the directory must be kept: %v", err)
		}
	})
}

func TestMigrationSecretsWipe_Registered(t *testing.T) {
	if !slices.Contains(Default.Commands(), "migration.secrets_wipe") {
		t.Fatal("migration.secrets_wipe must be registered")
	}
}

func TestMigrationSecretsWipe_RejectsABadJobID(t *testing.T) {
	for _, id := range []string{"", "../../etc/shadow", "01KZ000000000000000000000/", "01KZ00000000000000000000000"} {
		raw, _ := json.Marshal(map[string]string{"job_id": id})
		_, err := migrationSecretsWipeHandler(context.Background(), raw)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("job_id %q: err=%v, want an invalid-argument error", id, err)
		}
	}
}
