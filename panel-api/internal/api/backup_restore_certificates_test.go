package api

import (
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993, JAB-54: a restore from this server's own backup destinations
// installs the SSL certificates the backup carries. One from an uploaded file
// does only when the admin checks "Keep the backup's SSL certificates"; a full
// server restore, which has no such choice, never does.

func TestRestoreMetadataDeps_RestoreCertificates(t *testing.T) {
	h := &backupHandler{cfg: ucConfig()}
	for _, tc := range []struct {
		name     string
		uploaded *uploadedData
		want     bool
	}{
		{"this server's own backup", nil, true},
		{"an uploaded file", &uploadedData{}, false},
		{"an uploaded file, certificates kept", &uploadedData{keepCertificates: true}, true},
	} {
		if got := h.restoreMetadataDeps(tc.uploaded).RestoreCertificates; got != tc.want {
			t.Errorf("%s: RestoreCertificates = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRestoreUploadApply_KeepCertificates(t *testing.T) {
	for _, keepCerts := range []bool{false, true} {
		rebuilds := captureRebuilds(t)
		restoreUploadDir = t.TempDir()
		t.Cleanup(func() { restoreUploadDir = "/var/lib/jabali-uploads" })
		var restores []map[string]any
		cfg := ucConfig()
		cfg.Agent = keAgent(capsNew, &restores)
		cfg.Users = ubUsers{}
		cfg.ServerSettings = &fakeSettingsRepo{s: allFeaturesOn()}
		h := &backupHandler{cfg: cfg}
		stage(t, "upload0001")

		w := keRequest(t, h.restoreUploadApply, ubAdmin, true,
			map[string]any{"upload_id": "upload0001", "target_username": "alice", "keep_certificates": keepCerts})
		if w.Code != http.StatusAccepted {
			t.Fatalf("keep=%v: status %d body %s", keepCerts, w.Code, w.Body)
		}
		waitUploadOutcome(t, restoreUploadOutcomePath(ubAdmin, "upload0001"))
		if got := rebuilds(); len(got) != 1 || got[0].keepCertificates != keepCerts {
			t.Fatalf("keep=%v: the rebuild got %+v", keepCerts, got)
		}
	}
}

func TestUploadedBackupRestore_KeepCertificates(t *testing.T) {
	for _, keepCerts := range []bool{false, true} {
		rebuilds := captureRebuilds(t)
		e := newUBEnv(t, &ubAgent{})
		b := keep(t, e, models.UploadedBackupKeep)
		w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore",
			map[string]any{"target_username": "alice", "keep_certificates": keepCerts})
		if w.Code != http.StatusAccepted {
			t.Fatalf("keep=%v: restore %d %s", keepCerts, w.Code, w.Body)
		}
		waitRestore(t, e, b.ID)
		if got := rebuilds(); len(got) != 1 || got[0].keepCertificates != keepCerts {
			t.Fatalf("keep=%v: the rebuild got %+v", keepCerts, got)
		}
	}
}
