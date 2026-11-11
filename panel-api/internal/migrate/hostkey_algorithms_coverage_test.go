package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every migration SSH dial that pins the source host key must also ask for
// ed25519 first. x/crypto's default order puts ECDSA and RSA before ED25519,
// so a dialer that leaves HostKeyAlgorithms unset sees the source's ECDSA key
// and refuses an ed25519 pin taken with `ssh-keyscan -t ed25519`, which is
// what the wizard tells the operator to run. Like the SecretRef check next to
// it, the failure mode is omission, so the guard is structural.
func TestEveryPinnedDialAsksForEd25519First(t *testing.T) {
	t.Parallel()
	const pin = "migrate.PinningHostKeyCallback("
	const algos = "HostKeyAlgorithms: migrate.HostKeyAlgorithms()"

	var offenders []string
	checked := 0
	err := filepath.Walk(".", func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		s := string(body)
		pins := strings.Count(s, pin)
		if pins == 0 {
			return nil
		}
		checked += pins
		if strings.Count(s, algos) != pins || strings.Count(s, "migrate.SSHHandshake(") != pins || strings.Contains(s, "ssh.NewClientConn(") {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if checked == 0 {
		t.Fatal("found no PinningHostKeyCallback uses — the dial pattern has changed and this test checks nothing")
	}
	for _, o := range offenders {
		t.Errorf("%s pins the host key without HostKeyAlgorithms: migrate.HostKeyAlgorithms() and migrate.SSHHandshake (or still calls ssh.NewClientConn)", o)
	}
}
