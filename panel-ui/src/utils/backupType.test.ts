import { describe, expect, it } from "vitest";

import { backupTypeLabel } from "./backupType";

describe("backupTypeLabel (GH #454)", () => {
  it("labels a database-only account backup as Database Backup, not Account", () => {
    expect(backupTypeLabel("account_backup", "database")).toBe("Database Backup");
  });

  it("labels files/folders content as Files Backup", () => {
    expect(backupTypeLabel("account_backup", "files")).toBe("Files Backup");
    expect(backupTypeLabel("account_backup", "folders")).toBe("Files Backup");
  });

  it("labels full/empty account content as Account Backup", () => {
    expect(backupTypeLabel("account_backup", "full")).toBe("Account Backup");
    expect(backupTypeLabel("account_backup", "")).toBe("Account Backup");
    expect(backupTypeLabel("account_backup", undefined)).toBe("Account Backup");
    expect(backupTypeLabel("account_backup", null)).toBe("Account Backup");
  });

  it("labels system backups/restores by kind regardless of content", () => {
    expect(backupTypeLabel("system_backup", "full")).toBe("Full Server Backup");
    expect(backupTypeLabel("system_restore", "full")).toBe("Server Restore");
    expect(backupTypeLabel("account_restore", "database")).toBe("Account Restore");
  });
});
