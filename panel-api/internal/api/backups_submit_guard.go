package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Double-submit guards for the starts that hand privileged work to the agent.
// A second click, a second tab or a retried request must not queue the same
// work twice. In-memory: the panel is one process.
var (
	// backupSubmits serialises the "already queued or running?" check and
	// the insert of a manual backup or restore for one target. The slot is
	// held for the request; the job row is what refuses later submits.
	backupSubmits = hostreserve.NewKeyedSemaphore(1, 0)
	// fullBackupPacks holds a run's slot while its package is being built.
	fullBackupPacks = hostreserve.NewKeyedSemaphore(1, 0)
	// phpInstalls holds a version's slot while it is being installed.
	phpInstalls = hostreserve.NewKeyedSemaphore(1, 0)
)

// claimBackupSubmit takes the submit slot for (userID, kind) and refuses when
// userID already has a pending job of kind (HasActiveOfKind). When ok is false
// the response is written. Otherwise the caller calls release once its insert
// is done (a defer in the handler is fine).
func claimBackupSubmit(c *gin.Context, jobs repository.BackupJobRepository, logErr func(string, error, ...any),
	userID, kind, code, detail string) (release func(), ok bool) {
	release, ok = backupSubmits.TryAcquire(kind + ":" + userID)
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": code, "detail": detail})
		return nil, false
	}
	active, err := jobs.HasActiveOfKind(c.Request.Context(), userID, kind)
	if err != nil {
		release()
		logErr("check active backup jobs", err, "user_id", userID, "kind", kind)
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "job_check_unavailable", "detail": "could not check for a job already in progress; try again"})
		return nil, false
	}
	if active {
		release()
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": code, "detail": detail})
		return nil, false
	}
	return release, true
}
