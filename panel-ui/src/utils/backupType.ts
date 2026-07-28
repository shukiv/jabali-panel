// backupType — GH #454: describe a backup job by WHAT it captured, not just the
// operation kind. A backup carries both `kind` (account_backup / system_backup /
// *_restore) and `content` (full / files / database / folders). The admin list
// used to show only the kind, so a database-only account backup read as
// "Account Backup". Combine both into one accurate label.

// backupTypeLabel maps (kind, content) to a human "Type".
//   system_backup (+accounts) -> Full Server Backup; alone -> System Backup
//   account_backup + database -> Database Backup
//   account_backup + files/folders -> Files Backup
//   account_backup + full/""  -> Account Backup
//   *_restore               -> "… Restore"
export function backupTypeLabel(kind: string, content?: string | null, hasAccounts?: boolean): string {
  switch (kind) {
    case "system_backup":
      // GH #502: a system_backup run that fanned out account backups is a Full
      // Server backup; one on its own (include_accounts off) is System-only.
      return hasAccounts ? "Full Server Backup" : "System Backup";
    case "system_restore":
      return "Server Restore";
    case "account_restore":
      return "Account Restore";
  }
  // account_backup (and any unknown backup kind) — describe by content.
  switch (content) {
    case "database":
      return "Database Backup";
    case "files":
    case "folders":
      return "Files Backup";
    default: // "full", "", null/undefined
      return "Account Backup";
  }
}

// runScopeSummary (GH #502): a one-line scope summary for a collapsed backup
// RUN row, so a Full Server backup reads as one logical backup
// ("system + N accounts") without expanding it to count the child snapshots.
// `total` is all jobs in the run; `accounts` is the per-account snapshot count.
export function runScopeSummary(total: number, accounts: number): string {
  const hasSystem = total > accounts; // non-account jobs = the system backup
  if (accounts > 0 && hasSystem) {
    return `system + ${accounts} account${accounts === 1 ? "" : "s"}`;
  }
  if (accounts > 0) {
    return `${accounts} account${accounts === 1 ? "" : "s"}`;
  }
  return ""; // system-only run — the type tag already says it
}

// backupTypeColor picks an AntD Tag color for the label.
export function backupTypeColor(kind: string, content?: string | null): string {
  if (kind === "system_backup") return "purple";
  if (kind === "system_restore" || kind === "account_restore") return "orange";
  switch (content) {
    case "database":
      return "geekblue";
    case "files":
    case "folders":
      return "cyan";
    default:
      return "blue";
  }
}
