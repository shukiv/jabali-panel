package commands

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// system_fullbackup.restore — GH #1408 phase 2. An UPLOADED full-server
// container (produced by system.fullbackup.pack) is a PLAIN tar of
// manifest.json + system.tar.zst + users/<username>.tar.zst, where each inner
// tar is byte-identical to a per-account backup. This file inspects one; the
// panel restores it by extracting it (system.fullbackup.extract_uploaded) and
// restoring each inner tar through backup.restore_from_tar in mode=upload
// (GH #1993), never unconfined.
//
// The container itself is untrusted (safe extractor), and each inner tar is
// untrusted again (restoreAccountFromTar's hardened zstd extractor). System
// restore is deliberately NOT applied from an upload: it changes server config
// and stays a manual/CLI, step-up-worthy operation.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func randomULID() string {
	rnd := make([]byte, 26)
	_, _ = rand.Read(rnd)
	b := make([]byte, 26)
	for i := range b {
		b[i] = crockford[int(rnd[i])%32]
	}
	return string(b)
}

type fullContainerManifest struct {
	RunID   string `json:"run_id"`
	Schema  int    `json:"schema"`
	Entries []struct {
		Username string `json:"username,omitempty"`
		Label    string `json:"label"`
		JobID    string `json:"job_id"`
	} `json:"entries"`
}

func fullContainerPathClean(tarPath string) (string, error) {
	clean := filepath.Clean(tarPath)
	if clean != tarPath || !strings.HasPrefix(clean, restoreUploadsRoot+"/") {
		return "", bkInvalidArg("tar_path must be a clean path under " + restoreUploadsRoot)
	}
	if fi, err := os.Lstat(clean); err != nil || !fi.Mode().IsRegular() {
		return "", bkInvalidArg("tar_path is not a regular file")
	}
	return clean, nil
}

type systemFullbackupInspectParams struct {
	TarPath string `json:"tar_path"`
}

// system.fullbackup.inspect_uploaded reads the container's manifest (no full
// extraction) so the UI can offer System + which users to restore.
func systemFullbackupInspectUploadedHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p systemFullbackupInspectParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg(fmt.Sprintf("invalid params: %v", err))
	}
	clean, aerr := fullContainerPathClean(p.TarPath)
	if aerr != nil {
		return nil, aerr
	}
	mb, err := readFileFromPlainTar(clean, "manifest.json")
	if err != nil {
		return nil, bkInvalidArg("not a full-server backup container (no manifest.json): " + err.Error())
	}
	var man fullContainerManifest
	if json.Unmarshal(mb, &man) != nil {
		return nil, bkInvalidArg("container manifest parse failed")
	}
	users := make([]string, 0, len(man.Entries))
	hasSystem := false
	for _, e := range man.Entries {
		if e.Label == "system" {
			hasSystem = true
			continue
		}
		if e.Username != "" {
			users = append(users, e.Username)
		}
	}
	return map[string]any{"run_id": man.RunID, "users": users, "has_system": hasSystem}, nil
}

// system.fullbackup.restore_uploaded (which restored every account of an
// uploaded container unconfined, as root) is gone: the panel restores each
// account through backup.restore_from_tar in mode=upload (GH #1993).

func init() {
	Default.Register("system.fullbackup.inspect_uploaded", systemFullbackupInspectUploadedHandler)
}
