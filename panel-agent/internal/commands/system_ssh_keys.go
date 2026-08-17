// M30.1 follow-up — agent-side ssh-key management. panel-api runs as
// the jabali user under ProtectHome=true + ProtectSystem=strict so it
// cannot read /root/.ssh or write /etc/jabali-panel/restic-remotes/.
// All such operations are delegated here to the agent (root).
//
// Commands:
//   system.sshkeys.list       — list /root/.ssh/id_* private keys + pubkey body
//   system.sshkeys.generate   — ssh-keygen a new pair under /root/.ssh
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const sshKeysDir = "/root/.ssh"

type sshKeyEntry struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	PubkeyPath    string `json:"pubkey_path"`
	Pubkey        string `json:"pubkey"`
	HasPassphrase bool   `json:"has_passphrase"`
}

type sshKeysListResult struct {
	Keys []sshKeyEntry `json:"keys"`
}

func systemSSHKeysListHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	entries, err := os.ReadDir(sshKeysDir)
	if err != nil {
		if os.IsNotExist(err) {
			return sshKeysListResult{Keys: []sshKeyEntry{}}, nil
		}
		return nil, fmt.Errorf("readdir %s: %w", sshKeysDir, err)
	}
	out := []sshKeyEntry{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "id_") || strings.HasSuffix(name, ".pub") {
			continue
		}
		priv := filepath.Join(sshKeysDir, name)
		pub := priv + ".pub"
		entry := sshKeyEntry{Name: name, Path: priv, PubkeyPath: pub}
		if body, err := os.ReadFile(pub); err == nil {
			entry.Pubkey = strings.TrimSpace(string(body))
		}
		entry.HasPassphrase = sshKeyHasPassphrase(priv)
		out = append(out, entry)
	}
	return sshKeysListResult{Keys: out}, nil
}

func sshKeyHasPassphrase(privPath string) bool {
	cmd := execCommand("ssh-keygen", "-y", "-P", "", "-f", privPath) //nolint:gosec
	if err := cmd.Run(); err != nil {
		return true
	}
	return false
}

type sshKeyGenerateParams struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type sshKeyGenerateResult struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	PubkeyPath string `json:"pubkey_path"`
	Pubkey     string `json:"pubkey"`
}

func systemSSHKeysGenerateHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p sshKeyGenerateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid_arg: %w", err)
	}
	name := strings.TrimSpace(p.Name)
	if !validSSHKeyName(name) {
		return nil, fmt.Errorf("invalid_arg: name must match id_[A-Za-z0-9_]+ with no path separators")
	}
	keyType := p.Type
	if keyType == "" {
		keyType = "ed25519"
	}
	if keyType != "ed25519" && keyType != "rsa" {
		return nil, fmt.Errorf("invalid_arg: type must be ed25519 or rsa")
	}
	if err := os.MkdirAll(sshKeysDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", sshKeysDir, err)
	}
	priv := filepath.Join(sshKeysDir, name)
	if _, err := os.Stat(priv); err == nil {
		return nil, fmt.Errorf("key_exists: %s already exists; rename or delete first", priv)
	}
	args := []string{"-t", keyType, "-f", priv, "-N", "", "-C", "jabali-backup"}
	if keyType == "rsa" {
		args = append(args, "-b", "4096")
	}
	cmd := execCommandContext(ctx, "ssh-keygen", args...) //nolint:gosec
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ssh-keygen failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	pub, _ := os.ReadFile(priv + ".pub")
	return sshKeyGenerateResult{
		Name:       name,
		Path:       priv,
		PubkeyPath: priv + ".pub",
		Pubkey:     strings.TrimSpace(string(pub)),
	}, nil
}

func validSSHKeyName(name string) bool {
	if !strings.HasPrefix(name, "id_") {
		return false
	}
	if strings.ContainsAny(name, "/\\.") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}

func init() {
	Default.Register("system.sshkeys.list", systemSSHKeysListHandler)
	Default.Register("system.sshkeys.generate", systemSSHKeysGenerateHandler)
}
