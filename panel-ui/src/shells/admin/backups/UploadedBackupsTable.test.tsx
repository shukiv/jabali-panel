// GH #1993: account backups uploaded from another server stay on this one and
// are listed under Backups, where they can be restored again or deleted.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { UploadedBackupsTable } from "./UploadedBackupsTable";
import type { UploadedBackup } from "../../../apiClient";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), delete: vi.fn() },
}));

vi.mock("../../../lib/feedback", () => ({
  feedback: {
    modal: { confirm: (o: { onOk?: () => void }) => o.onOk?.() },
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

function row(id: string, extra: Partial<UploadedBackup> = {}): UploadedBackup {
  return {
    id,
    file_name: `${id}.tar.zst`,
    size_bytes: 3 * 1024 * 1024 * 1024,
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
    create_supported: true,
    created_at: "2026-10-06T10:00:00Z",
    ...extra,
  };
}

function mockList(rows: UploadedBackup[]) {
  mocked.get.mockReset().mockImplementation(async (url: string) => {
    if (url.startsWith("/admin/uploaded-backups")) return { data: { data: rows, total: rows.length } };
    throw new Error(`unexpected GET ${url}`);
  });
}

function renderTable(onRestore = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const r = render(
    <QueryClientProvider client={qc}>
      <App>
        <UploadedBackupsTable onRestore={onRestore} />
      </App>
    </QueryClientProvider>,
  );
  return { ...r, onRestore };
}

function rowOf(text: string): HTMLElement {
  const tr = screen.getByText(text).closest("tr");
  if (!tr) throw new Error(`no row for ${text}`);
  return tr as HTMLElement;
}

beforeEach(() => {
  mocked.delete.mockReset().mockResolvedValue({ data: { status: "ok" } });
});

describe("UploadedBackupsTable (GH #1993)", () => {
  it("lists each upload with how long it is kept and its last restore", async () => {
    mockList([
      row("keep1"),
      row("week1", { retention: "keep_7_days", expires_at: "2026-10-13T10:00:00Z" }),
      row("gone1", {
        retention: "delete_after_restore",
        file_present: false,
        restore_status: "done",
        restore_target: "alice",
        restored_at: "2026-10-06T11:00:00Z",
      }),
      row("fail1", { restore_status: "failed", restore_target: "bob", restore_result: { error: "disk full" } }),
    ]);
    renderTable();
    await screen.findByText("Uploaded backups");
    expect(screen.getByText("Uploaded — created on another server")).toBeTruthy();
    expect(within(rowOf("keep1.tar.zst")).getByText("Until deleted")).toBeTruthy();
    expect(within(rowOf("keep1.tar.zst")).getByText("Not restored yet")).toBeTruthy();
    expect(within(rowOf("keep1.tar.zst")).getByText("3.0 GB")).toBeTruthy();
    expect(within(rowOf("week1.tar.zst")).getByText(/^Until 13 Oct 2026/)).toBeTruthy();
    expect(within(rowOf("gone1.tar.zst")).getByText("Deleted after the restore")).toBeTruthy();
    expect(within(rowOf("gone1.tar.zst")).getByText("Restored into alice")).toBeTruthy();
    expect(within(rowOf("fail1.tar.zst")).getByText("Restore into bob failed")).toBeTruthy();
  });

  it("restores a kept upload, but not one whose file is gone or that is restoring", async () => {
    const kept = row("keep1");
    mockList([kept, row("gone1", { file_present: false }), row("busy1", { restore_status: "restoring", restore_target: "alice" })]);
    const { onRestore } = renderTable();
    await screen.findByText("Uploaded backups");

    fireEvent.click(within(rowOf("keep1.tar.zst")).getByText("Restore"));
    expect(onRestore).toHaveBeenCalledWith(kept);

    for (const name of ["gone1.tar.zst", "busy1.tar.zst"]) {
      const btn = within(rowOf(name)).getByText("Restore").closest("button");
      expect(btn?.disabled, name).toBe(true);
    }
    // A restoring upload can't be deleted under the restore: Restore is its
    // only action, so there is no overflow menu holding a Delete.
    expect(within(rowOf("busy1.tar.zst")).getAllByRole("button")).toHaveLength(1);
    expect(within(rowOf("keep1.tar.zst")).getAllByRole("button")).toHaveLength(2);
  });

  it("Delete calls DELETE /admin/uploaded-backups/:id", async () => {
    mockList([row("keep1")]);
    renderTable();
    await screen.findByText("Uploaded backups");
    // The first action is Restore; Delete sits in the overflow menu.
    const more = within(rowOf("keep1.tar.zst")).getAllByRole("button").at(-1)!;
    fireEvent.mouseEnter(more);
    fireEvent.click(more);
    fireEvent.click(await screen.findByText("Delete"));
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/admin/uploaded-backups/keep1"));
  });

  it("is hidden while there are no uploads", async () => {
    mockList([]);
    const { container } = renderTable();
    await waitFor(() => expect(mocked.get).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });
});
