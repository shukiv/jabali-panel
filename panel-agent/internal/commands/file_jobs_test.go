package commands

import (
	"testing"
	"time"
)

func resetFileJobs() {
	fileJobs.mu.Lock()
	fileJobs.m = map[string]*fileJob{}
	fileJobs.mu.Unlock()
}

func TestFileJobs_OwnershipAndLifecycle(t *testing.T) {
	resetFileJobs()
	j, err := newFileJob("alice", "extract")
	if err != nil || j == nil {
		t.Fatalf("newFileJob: %v", err)
	}
	// Ownership: another user must never see alice's job.
	if getFileJob(j.id, "bob") != nil {
		t.Fatal("bob must not see alice's job")
	}
	// Unknown id → nil.
	if getFileJob("00000000000000000000000000000000", "alice") != nil {
		t.Fatal("unknown id must be nil")
	}
	// Owner → job.
	if getFileJob(j.id, "alice") == nil {
		t.Fatal("alice must see her own job")
	}
	// Progress.
	j.setTotal(10)
	j.tick(3)
	if s := j.snapshot(); s.Total != 10 || s.Done != 3 || s.Status != fileJobRunning || s.JobID != j.id {
		t.Fatalf("running snapshot = %+v", s)
	}
	// Finish carries the result and pins done to extracted+skipped.
	j.finish(filesExtractResult{Extracted: 8, Skipped: 2})
	if s := j.snapshot(); s.Status != fileJobDone || s.Done != 10 || s.Result.Extracted != 8 || s.Result.Skipped != 2 {
		t.Fatalf("done snapshot = %+v", s)
	}
}

func TestFileJobs_FailCarriesMessage(t *testing.T) {
	resetFileJobs()
	j, _ := newFileJob("u", "extract")
	j.fail("not a valid zip")
	if s := j.snapshot(); s.Status != fileJobError || s.Error != "not a valid zip" {
		t.Fatalf("fail snapshot = %+v", s)
	}
}

func TestFileJobs_PerUserCap(t *testing.T) {
	resetFileJobs()
	for i := 0; i < maxJobsPerUser; i++ {
		if _, err := newFileJob("cap", "extract"); err != nil {
			t.Fatalf("job %d rejected early: %v", i, err)
		}
	}
	if _, err := newFileJob("cap", "extract"); err == nil {
		t.Fatal("the (maxJobsPerUser+1)th running job must be rejected")
	}
	// A different user is unaffected by another's cap.
	if _, err := newFileJob("other", "extract"); err != nil {
		t.Fatalf("other user must not be capped: %v", err)
	}
	// A finished job frees a slot.
	fileJobs.mu.Lock()
	for _, j := range fileJobs.m {
		if j.username == "cap" {
			j.mu.Lock()
			done := j.status == fileJobRunning
			j.mu.Unlock()
			if done {
				j.finish(filesExtractResult{})
				break
			}
		}
	}
	fileJobs.mu.Unlock()
	if _, err := newFileJob("cap", "extract"); err != nil {
		t.Fatalf("a freed slot must accept a new job: %v", err)
	}
}

func TestFileJobs_ReapFinished(t *testing.T) {
	resetFileJobs()
	j, _ := newFileJob("reap", "extract")
	j.finish(filesExtractResult{})
	j.mu.Lock()
	j.startedAt = time.Now().Add(-fileJobTTL - time.Minute)
	j.mu.Unlock()
	// getFileJob runs reapLocked first, so the stale finished job is gone.
	if getFileJob(j.id, "reap") != nil {
		t.Fatal("a finished job past its TTL must be reaped")
	}
	// A running job is never reaped, however old.
	r, _ := newFileJob("reap", "extract")
	r.mu.Lock()
	r.startedAt = time.Now().Add(-fileJobTTL - time.Hour)
	r.mu.Unlock()
	if getFileJob(r.id, "reap") == nil {
		t.Fatal("a running job must not be reaped")
	}
}
