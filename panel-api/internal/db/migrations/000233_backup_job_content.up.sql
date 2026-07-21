-- GH #454 Step 4: denormalise the schedule's content onto each backup_job.
-- backup_jobs.content records what a run captured (full/files/database/folders,
-- the GH #294 selectors). The scheduler stamps it from the schedule at enqueue
-- time so the dispatcher trims the db/mailbox lists to match, and the history UI
-- shows what each restore point contains without re-loading the schedule.
-- Default 'full' preserves the pre-feature behaviour for existing rows.
ALTER TABLE backup_jobs
  ADD COLUMN content VARCHAR(16) NOT NULL DEFAULT 'full';
