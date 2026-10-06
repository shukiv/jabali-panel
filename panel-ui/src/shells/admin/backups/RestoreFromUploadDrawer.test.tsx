// GH #1993: an admin upload is kept on the server with the chosen retention
// and restored from there, so a failed restore is retried without uploading
// again. The tenant (ownerMode) flow is unchanged.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { RestoreFromUploadDrawer } from "./RestoreFromUploadDrawer";
import type { UploadedBackup } from "../../../apiClient";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(async () => ({ data: { data: [], total: 0 } })) },
  uploadBackupArchiveChunked: vi.fn(),
  inspectUploadedBackup: vi.fn(),
  applyUploadedBackupRestore: vi.fn(),
  registerUploadedBackup: vi.fn(),
  getUploadedBackup: vi.fn(),
  restoreKeptUploadedBackup: vi.fn(),
}));

vi.mock("../../../lib/feedback", () => ({
  feedback: {
    modal: { confirm: (o: { onOk?: () => void }) => o.onOk?.() },
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  },
}));

import * as api from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

const MOCKED = [
  "uploadBackupArchiveChunked",
  "inspectUploadedBackup",
  "applyUploadedBackupRestore",
  "registerUploadedBackup",
  "getUploadedBackup",
  "restoreKeptUploadedBackup",
] as const;
const m = api as unknown as Record<(typeof MOCKED)[number], ReturnType<typeof vi.fn>>;

const KEPT: UploadedBackup = {
  id: "01K00000000000000000000001",
  file_name: "alice.tar.zst",
  size_bytes: 1024,
  account_username: "alice",
  account_email: "alice@example.com",
  components: ["home", "db", "mail"],
  retention: "keep",
  expires_at: null,
  uploaded_by: "admin1",
  restore_status: "",
  restore_started_at: null,
  restored_at: null,
  restore_target: "",
  file_present: true,
  target_exists: true,
  create_supported: true,
  created_at: "2026-10-06T10:00:00Z",
};

function renderDrawer(props: Partial<Parameters<typeof RestoreFromUploadDrawer>[0]> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onKeptChange = vi.fn();
  const r = render(
    <QueryClientProvider client={qc}>
      <App>
        <RestoreFromUploadDrawer open onClose={vi.fn()} onKeptChange={onKeptChange} {...props} />
      </App>
    </QueryClientProvider>,
  );
  return { ...r, onKeptChange };
}

function pickFile() {
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  const file = new File(["x"], "alice.tar.zst", { type: "application/zstd" });
  fireEvent.change(input, { target: { files: [file] } });
}

beforeEach(() => {
  for (const name of MOCKED) m[name].mockReset();
  vi.mocked(feedback.message.error).mockReset();
  m.uploadBackupArchiveChunked.mockResolvedValue("upload-1");
  m.registerUploadedBackup.mockImplementation(async (_id: string, retention: string) => ({ ...KEPT, retention }));
  m.getUploadedBackup.mockResolvedValue(KEPT);
  m.restoreKeptUploadedBackup.mockResolvedValue({ status: "ok", applied: ["home", "db"] });
  m.inspectUploadedBackup.mockResolvedValue({
    user: { id: "u1", username: "alice" },
    components: ["home", "db", "mail", "dns"],
  });
  m.applyUploadedBackupRestore.mockResolvedValue({ status: "ok", applied: ["home"] });
});

describe("RestoreFromUploadDrawer (GH #1993)", () => {
  it("keeps an admin upload with the chosen retention and restores it from the server", async () => {
    const { onKeptChange } = renderDrawer();
    expect(screen.getByText("Keep it on this server until I delete it")).toBeTruthy();
    fireEvent.click(screen.getByText("Delete it once a restore succeeds"));
    pickFile();
    fireEvent.click(screen.getByText(/Upload & inspect/));

    await screen.findByText("Backup of alice");
    expect(m.registerUploadedBackup).toHaveBeenCalledWith("upload-1", "delete_after_restore", "alice.tar.zst");
    expect(m.inspectUploadedBackup).not.toHaveBeenCalled();
    expect(onKeptChange).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Mail (mailboxes and messages)")).toBeTruthy();

    fireEvent.click(screen.getByText("Restore into alice"));
    await waitFor(() =>
      expect(m.restoreKeptUploadedBackup).toHaveBeenCalledWith(
        KEPT.id,
        "alice",
        ["home", "db", "mail"],
        undefined,
        expect.any(Function),
      ),
    );
    expect(m.applyUploadedBackupRestore).not.toHaveBeenCalled();
    await screen.findByText("Restore result");
    expect(onKeptChange).toHaveBeenCalledTimes(2);
  });

  it("says a kept upload survives a failed restore", async () => {
    m.restoreKeptUploadedBackup.mockRejectedValue(new Error("disk full"));
    renderDrawer({ uploaded: KEPT });
    fireEvent.click(await screen.findByText("Restore into alice"));
    await waitFor(() =>
      expect(feedback.message.error).toHaveBeenCalledWith(
        "disk full — the uploaded backup is kept; restore it again from Backups",
      ),
    );
  });

  it("restores a kept upload without asking for a file", async () => {
    renderDrawer({ uploaded: KEPT });
    await screen.findByText("Backup of alice");
    expect(m.getUploadedBackup).toHaveBeenCalledWith(KEPT.id);
    expect(screen.queryByText(/Upload & inspect/)).toBeNull();
    expect(screen.getByText("Restore uploaded backup")).toBeTruthy();
    fireEvent.click(screen.getByText("Restore into alice"));
    await screen.findByText("Restore result");
    expect(screen.getByText("The uploaded backup stays listed under Backups.")).toBeTruthy();
    expect(m.uploadBackupArchiveChunked).not.toHaveBeenCalled();
  });

  it("offers to create the account when it is not on this server", async () => {
    m.getUploadedBackup.mockResolvedValue({ ...KEPT, target_exists: false });
    renderDrawer({ uploaded: KEPT });
    fireEvent.click(await screen.findByText("Create alice & restore"));
    await waitFor(() =>
      expect(m.restoreKeptUploadedBackup).toHaveBeenCalledWith(
        KEPT.id,
        "alice",
        KEPT.components,
        { createUser: true, packageId: null },
        expect.any(Function),
      ),
    );
  });

  it("leaves the tenant restore as it was: no retention, inspect + apply", async () => {
    renderDrawer({ ownerMode: true });
    expect(screen.queryByText("Keep it on this server until I delete it")).toBeNull();
    pickFile();
    fireEvent.click(screen.getByText(/Upload & inspect/));
    await screen.findByText("Backup of alice");
    expect(m.inspectUploadedBackup).toHaveBeenCalledWith("upload-1", "/me/backups");
    expect(m.registerUploadedBackup).not.toHaveBeenCalled();
    fireEvent.click(screen.getByText("Restore into my account"));
    await waitFor(() =>
      expect(m.applyUploadedBackupRestore).toHaveBeenCalledWith(
        "upload-1",
        "alice",
        ["home", "db", "mail"],
        "/me/backups",
        undefined,
        expect.any(Function),
      ),
    );
  });

  it("shows the running restore's step and what it is doing", async () => {
    m.restoreKeptUploadedBackup.mockImplementation(
      (_id: string, _u: string, _c: string[], _o: unknown, onProgress: (p: unknown) => void) => {
        onProgress({
          step: 1,
          steps: 3,
          label: "Restoring files, databases and apps",
          detail: "Restoring database alice_wp (2 of 3)",
          percent: 33,
        });
        return new Promise(() => {}); // still running
      },
    );
    renderDrawer({ uploaded: KEPT });
    fireEvent.click(await screen.findByText("Restore into alice"));
    expect(await screen.findByText("Step 1 of 3: Restoring files, databases and apps")).toBeTruthy();
    expect(screen.getByText("Restoring database alice_wp (2 of 3)")).toBeTruthy();
    // 1 of 3 steps at 33% → 11% of the whole restore.
    expect(document.querySelector(".ant-progress")?.getAttribute("aria-valuenow")).toBe("11");
  });
});
