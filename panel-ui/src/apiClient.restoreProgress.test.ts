// GH #1993: both restore pollers hand the running restore's progress to the
// caller, so the drawer can show the step instead of a spinner.
import { afterEach, describe, expect, it, vi } from "vitest";

import { apiClient, applyUploadedBackupRestore, restoreKeptUploadedBackup } from "./apiClient";

const P = { step: 2, steps: 3, label: "Rebuilding the account's domains, mailboxes and settings" };

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("restore pollers report progress (GH #1993)", () => {
  it("restoreKeptUploadedBackup", async () => {
    vi.useFakeTimers();
    vi.spyOn(apiClient, "post").mockResolvedValue({ data: {} });
    vi.spyOn(apiClient, "get")
      .mockResolvedValueOnce({ data: { data: { restore_status: "restoring", restore_progress: P } } })
      .mockResolvedValueOnce({ data: { data: { restore_status: "done", restore_result: { applied: ["home"] } } } });
    const seen: unknown[] = [];
    const done = restoreKeptUploadedBackup("01K00000000000000000000001", "alice", ["home"], undefined, (p) => seen.push(p));
    await vi.advanceTimersByTimeAsync(5000);
    await expect(done).resolves.toMatchObject({ status: "ok", applied: ["home"] });
    expect(seen).toEqual([P]);
  });

  it("applyUploadedBackupRestore", async () => {
    vi.useFakeTimers();
    vi.spyOn(apiClient, "post").mockResolvedValue({ data: {} });
    vi.spyOn(apiClient, "get")
      .mockResolvedValueOnce({ data: { status: "restoring", progress: P } })
      .mockResolvedValueOnce({ data: { status: "done", applied: ["home"] } });
    const seen: unknown[] = [];
    const done = applyUploadedBackupRestore("upload-1", "alice", ["home"], "/me/backups", undefined, (p) => seen.push(p));
    await vi.advanceTimersByTimeAsync(5000);
    await expect(done).resolves.toMatchObject({ status: "ok", applied: ["home"] });
    expect(seen).toEqual([P]);
  });
});
