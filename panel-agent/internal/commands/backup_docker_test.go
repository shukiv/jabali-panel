package commands

// Docker app data lives at /var/lib/jabali/docker-apps/<slug>, outside the
// user home, so the home stage never walked it (GH #954). An account with a
// docker app backed up without its app data — and because the account backup
// is also the transport for Jabali→Jabali migration and the DR standby, the
// app silently failed to arrive on the destination.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

const testJobID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"
const testUserID = "01HZZZZZZZZZZZZZZZZZZZZZZY"

// The wire contract: panel-api sends docker_apps, the agent must actually
// read it. A silent field-name mismatch here would produce a backup that
// looks complete and carries nothing.
func TestBackupCreateParams_ReadsDockerApps(t *testing.T) {
	var req backupCreateParams
	body := `{"job_id":"` + testJobID + `","user_id":"` + testUserID + `",` +
		`"username":"alice","docker_apps":["nextcloud","uptime-kuma"]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.DockerApps) != 2 || req.DockerApps[0] != "nextcloud" {
		t.Fatalf("docker_apps did not decode into DockerApps: %#v", req.DockerApps)
	}
}

func TestRunDockerStage_SkipsWhenNoApps(t *testing.T) {
	got := runDockerStage(context.Background(), backupCreateParams{
		JobID: testJobID, UserID: testUserID, Username: "alice",
	})
	if len(got) != 1 || got[0].Status != backup.StageStatusSkipped {
		t.Fatalf("want a single skipped stage, got %#v", got)
	}
	if got[0].Name != backup.StageDocker {
		t.Errorf("stage name = %q, want %q", got[0].Name, backup.StageDocker)
	}
}

// content=database is a DB-only backup; app data is file data, so it is
// excluded for the same reason the home stage is.
func TestRunDockerStage_SkipsOnDatabaseContent(t *testing.T) {
	got := runDockerStage(context.Background(), backupCreateParams{
		JobID: testJobID, UserID: testUserID, Username: "alice",
		DockerApps: []string{"nextcloud"}, Content: "database",
	})
	if len(got) != 1 || got[0].Status != backup.StageStatusSkipped {
		t.Fatalf("want a single skipped stage, got %#v", got)
	}
	if len(got[0].Warnings) == 0 || !strings.Contains(got[0].Warnings[0], "content=database") {
		t.Errorf("skip reason should name content=database, got %v", got[0].Warnings)
	}
}

// The slug is interpolated into a filesystem path, so a traversal attempt
// must be refused before anything touches disk.
func TestBackupDockerHandler_RejectsSlugTraversal(t *testing.T) {
	for _, bad := range []string{"../../etc", "foo/bar", "foo bar", ".."} {
		body, _ := json.Marshal(backupDockerParams{
			JobID: testJobID, UserID: testUserID, Username: "alice",
			DockerApps: []string{bad},
		})
		if _, err := backupDockerHandler(context.Background(), body); err == nil {
			t.Errorf("slug %q was accepted; it reaches a filesystem path", bad)
		}
	}
}

func TestBackupDockerHandler_RejectsBadJobID(t *testing.T) {
	body, _ := json.Marshal(backupDockerParams{
		JobID: "not-a-ulid", UserID: testUserID, Username: "alice",
		DockerApps: []string{"nextcloud"},
	})
	if _, err := backupDockerHandler(context.Background(), body); err == nil {
		t.Error("expected a bad job_id to be refused")
	}
}

// A ManifestStage carries exactly one SnapshotID. Folding N apps into a
// single stage would leave restore able to materialize only the last one,
// so the stage must fan out one entry per app — same shape as db.
func TestRunDockerStage_FansOutPerApp(t *testing.T) {
	// dockerStagesFromResult is the real fan-out; driving it off a result
	// set keeps the test free of a restic repo.
	stages := dockerStagesFromResult(backupDockerResult{Snapshots: []backupDockerStageSnapshot{
		{App: "nextcloud", SnapshotID: "snapA", BytesAdded: 10, BytesTotal: 100},
		{App: "uptime-kuma", Error: "data dir missing"},
	}})
	if len(stages) != 2 {
		t.Fatalf("want one stage per app, got %d", len(stages))
	}
	if stages[0].SnapshotID != "snapA" || stages[0].Items[0] != "nextcloud" {
		t.Errorf("first stage lost its snapshot/app pairing: %#v", stages[0])
	}
	// The failed app must not silently vanish — restore reads Items to know
	// which slug a stage belongs to.
	if stages[1].Status != backup.StageStatusFailed || stages[1].Items[0] != "uptime-kuma" {
		t.Errorf("failed app should still be reported with its slug: %#v", stages[1])
	}
}

// stage=docker must be a distinct tag value: the restore walk and any
// tag-scoped restic query key off it.
func TestDockerStageTagIsDistinct(t *testing.T) {
	tags := backup.AccountBackupTags(testJobID, testUserID, "", backup.StageDocker)
	found := false
	for _, tg := range tags {
		if strings.Contains(string(tg), "stage") && strings.Contains(string(tg), "docker") {
			found = true
		}
	}
	if !found {
		t.Fatalf("stage=docker missing from account backup tags: %v", tags)
	}
	if backup.StageDocker == backup.StageHome || backup.StageDocker == backup.StageDB {
		t.Error("StageDocker collides with an existing stage value")
	}
}
