-- GH #1646: carry the restic compression level chosen for a Full Server backup
-- through to the per-account backup jobs it fans out.
--
-- backup_jobs.compression records the restic compression level ("" = auto /
-- "off" / "max", the GH #294 whitelist) denormalised onto the job at enqueue
-- time, mirroring the existing `content` column, so the dispatcher passes it to
-- the agent's backup.create without re-loading anything. Empty default keeps
-- every existing job and every account-path job on restic's current auto
-- behaviour (VARCHAR(8) is well under any row-size concern).
ALTER TABLE backup_jobs
    ADD COLUMN compression VARCHAR(8) NOT NULL DEFAULT '';
