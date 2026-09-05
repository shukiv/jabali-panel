// JAB-332: the exhaustive status × kind × snapshot eligibility matrix. This is
// the single source of truth for Download / Restore / Delete visibility on both
// the admin and tenant backup lists, so the cases below are hand-authored
// expectations (not derived from the implementation) — a regression that
// reopens the partial-download bug (GH #502) or lets Restore fire on a partial
// must fail here.
import { describe, expect, it } from "vitest";

import {
  backupArtifactEligibility,
  isRestoreKind,
  snapshotKnowledge,
  type SnapshotKnowledge,
} from "./backupArtifact";

const STATUSES = ["queued", "running", "succeeded", "partial", "failed", "cancelled"] as const;
const KINDS = ["account_backup", "system_backup", "account_restore", "system_restore"] as const;
const SNAPSHOTS: SnapshotKnowledge[] = ["present", "absent", "unknown"];

describe("snapshotKnowledge", () => {
  it("maps a missing field to unknown, empty to absent, a value to present", () => {
    expect(snapshotKnowledge(undefined)).toBe("unknown");
    expect(snapshotKnowledge(null)).toBe("absent");
    expect(snapshotKnowledge("")).toBe("absent");
    expect(snapshotKnowledge("snap-abc")).toBe("present");
  });
});

describe("isRestoreKind", () => {
  it("is true only for the two restore kinds", () => {
    expect(isRestoreKind("account_restore")).toBe(true);
    expect(isRestoreKind("system_restore")).toBe(true);
    expect(isRestoreKind("account_backup")).toBe(false);
    expect(isRestoreKind("system_backup")).toBe(false);
    expect(isRestoreKind(undefined)).toBe(false);
  });
});

// Hand-authored expectation per representative cell: [status, kind, snapshot] →
// { isRestore, canDownload, canRestore, canDelete }.
type Cell = {
  status: string;
  kind: string;
  snapshot: SnapshotKnowledge;
  isRestore: boolean;
  canDownload: boolean;
  canRestore: boolean;
  canDelete: boolean;
};

const CASES: Cell[] = [
  // account_backup — the common path.
  { status: "succeeded", kind: "account_backup", snapshot: "present", isRestore: false, canDownload: true, canRestore: true, canDelete: true },
  // GH #502 regression pin: a partial is downloadable but NOT restorable.
  { status: "partial", kind: "account_backup", snapshot: "present", isRestore: false, canDownload: true, canRestore: false, canDelete: true },
  { status: "failed", kind: "account_backup", snapshot: "present", isRestore: false, canDownload: false, canRestore: false, canDelete: true },
  { status: "queued", kind: "account_backup", snapshot: "absent", isRestore: false, canDownload: false, canRestore: false, canDelete: true },
  { status: "cancelled", kind: "account_backup", snapshot: "present", isRestore: false, canDownload: false, canRestore: false, canDelete: true },
  // Running is the only state that hides Delete (cancel first).
  { status: "running", kind: "account_backup", snapshot: "present", isRestore: false, canDownload: false, canRestore: false, canDelete: false },
  // A known-absent snapshot gates both Download and Restore even when succeeded
  // (the server 422s it — the button would be doomed).
  { status: "succeeded", kind: "account_backup", snapshot: "absent", isRestore: false, canDownload: false, canRestore: false, canDelete: true },
  // Unknown snapshot (the tenant payload has no snapshot_id) does NOT gate —
  // Download/Restore stay visible; the server enforces the snapshotless case.
  { status: "succeeded", kind: "account_backup", snapshot: "unknown", isRestore: false, canDownload: true, canRestore: true, canDelete: true },
  { status: "partial", kind: "account_backup", snapshot: "unknown", isRestore: false, canDownload: true, canRestore: false, canDelete: true },

  // system_backup — downloadable (Full Server system leg), never restorable via
  // the panel (account-only).
  { status: "succeeded", kind: "system_backup", snapshot: "present", isRestore: false, canDownload: true, canRestore: false, canDelete: true },
  { status: "partial", kind: "system_backup", snapshot: "present", isRestore: false, canDownload: true, canRestore: false, canDelete: true },
  { status: "succeeded", kind: "system_backup", snapshot: "absent", isRestore: false, canDownload: false, canRestore: false, canDelete: true },

  // account_restore / system_restore — history rows: no download, no restore,
  // deletable when not running.
  { status: "succeeded", kind: "account_restore", snapshot: "unknown", isRestore: true, canDownload: false, canRestore: false, canDelete: true },
  { status: "running", kind: "account_restore", snapshot: "unknown", isRestore: true, canDownload: false, canRestore: false, canDelete: false },
  { status: "succeeded", kind: "system_restore", snapshot: "present", isRestore: true, canDownload: false, canRestore: false, canDelete: true },
  { status: "failed", kind: "system_restore", snapshot: "unknown", isRestore: true, canDownload: false, canRestore: false, canDelete: true },
];

describe("backupArtifactEligibility — representative cells", () => {
  it.each(CASES)(
    "$status $kind snapshot=$snapshot",
    ({ status, kind, snapshot, isRestore, canDownload, canRestore, canDelete }) => {
      expect(backupArtifactEligibility({ status, kind, snapshot })).toEqual({
        isRestore,
        canDownload,
        canRestore,
        canDelete,
      });
    },
  );
});

describe("backupArtifactEligibility — invariants across all 72 cells", () => {
  it("holds the cross-cutting rules for every status × kind × snapshot", () => {
    for (const status of STATUSES) {
      for (const kind of KINDS) {
        for (const snapshot of SNAPSHOTS) {
          const e = backupArtifactEligibility({ status, kind, snapshot });

          // Delete is gated purely by running.
          expect(e.canDelete).toBe(status !== "running");

          // A restore-history row is never downloadable or restorable.
          if (e.isRestore) {
            expect(e.canDownload).toBe(false);
            expect(e.canRestore).toBe(false);
          }

          // Download only ever appears on a completed backup with a
          // non-absent snapshot — never on failed/queued/running/cancelled.
          if (e.canDownload) {
            expect(["succeeded", "partial"]).toContain(status);
            expect(snapshot).not.toBe("absent");
            expect(e.isRestore).toBe(false);
          }

          // Restore only ever appears on a fully-succeeded account backup with
          // a non-absent snapshot — never on a partial (GH #502).
          if (e.canRestore) {
            expect(status).toBe("succeeded");
            expect(kind).toBe("account_backup");
            expect(snapshot).not.toBe("absent");
          }
        }
      }
    }
  });

  it("defaults an omitted snapshot to unknown (does not gate)", () => {
    expect(backupArtifactEligibility({ status: "succeeded", kind: "account_backup" })).toEqual({
      isRestore: false,
      canDownload: true,
      canRestore: true,
      canDelete: true,
    });
  });
});
