// backupArtifact — the single, exhaustive eligibility matrix for a backup-list
// row (a backup artifact OR a restore-history entry), shared by the admin
// (AdminBackupsPage) and tenant (MyProfileBackupCard) backup lists.
//
// Both screens previously hand-wrote the Download / Restore / Delete visibility
// gates and drifted: the tenant let Restore fire on any succeeded row and never
// hid Delete on a running job, while the admin encoded the download bug's fix
// (GH #502: a `partial` backup is downloadable) in one place only. This module
// is the one place those gates live, so a future edit can't reopen the
// partial-download regression on one screen but not the other (JAB-332 AC5).
//
// The gates MIRROR the panel-api handlers that back each action, so a control
// the UI shows always corresponds to a request the server will honor:
//   - download: streamBackupArtifact / downloadPrepare gate on
//       status ∈ {succeeded, partial} AND snapshot_id != ""   (backups.go)
//   - restore : POST /admin/backups/restore is account-only and needs a
//       manifest snapshot                                       (backups.go)
//   - delete  : h.delete has no status gate; hiding Delete on a `running` job
//       is a UI guard (deleting mid-run orphans the systemd unit + restic
//       state) — admin offers Cancel for that state.

// A row's snapshot presence is three-valued, and the `unknown` state is
// load-bearing: the admin payload carries `snapshot_id` (so it knows present vs
// absent), but the tenant payload has no such field, so it declares `unknown`
// rather than letting an absent field read as "absent" and hide a valid
// Download. The server still hard-rejects a snapshotless job (422 no_snapshot_id),
// so `unknown` safely defers that one case to the backend.
export type SnapshotKnowledge = "present" | "absent" | "unknown";

// Map a raw snapshot_id (present in the admin payload, missing in the tenant's)
// to the three-valued knowledge the matrix consumes. `undefined` = the caller
// has no snapshot field at all (unknown); "" / null = the caller knows there is
// no snapshot (absent); a non-empty string = present.
export function snapshotKnowledge(id: string | null | undefined): SnapshotKnowledge {
  if (id === undefined) return "unknown";
  return id ? "present" : "absent";
}

// Restore-history rows (kind=account_restore / system_restore, GH #1044) share
// the backup list but own no artifact: they are neither downloadable nor
// re-restorable, and their "size" is meaningless.
export const RESTORE_KINDS: ReadonlySet<string> = new Set(["account_restore", "system_restore"]);

export const isRestoreKind = (kind: string | undefined): boolean =>
  kind !== undefined && RESTORE_KINDS.has(kind);

const DOWNLOADABLE_STATUSES: ReadonlySet<string> = new Set(["succeeded", "partial"]);

export interface BackupArtifactInput {
  status: string;
  /** account_backup / system_backup / account_restore / system_restore. */
  kind?: string;
  /** Defaults to "unknown" when the caller's payload has no snapshot field. */
  snapshot?: SnapshotKnowledge;
}

export interface BackupArtifactEligibility {
  /** A restore-history row, not a backup artifact — drives label/size/copy. */
  isRestore: boolean;
  canDownload: boolean;
  canRestore: boolean;
  canDelete: boolean;
}

export function backupArtifactEligibility(input: BackupArtifactInput): BackupArtifactEligibility {
  const snapshot = input.snapshot ?? "unknown";
  const isRestore = isRestoreKind(input.kind);
  const isBackup = !isRestore;

  // Download: any completed backup (succeeded OR partial — a partial run still
  // wrote a valid manifest snapshot, GH #502) whose snapshot is not known-absent.
  const canDownload =
    isBackup && DOWNLOADABLE_STATUSES.has(input.status) && snapshot !== "absent";

  // Restore: account backups only, and only a fully-succeeded one (a partial is
  // not a safe restore source), with a snapshot that is not known-absent.
  const canRestore =
    input.kind === "account_backup" && input.status === "succeeded" && snapshot !== "absent";

  // Delete: allowed for every finished state so failed/stale/cancelled rows can
  // be cleared; hidden only while running (cancel first).
  const canDelete = input.status !== "running";

  return { isRestore, canDownload, canRestore, canDelete };
}
