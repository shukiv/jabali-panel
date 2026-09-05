// JAB-332: the tenant backup card drives its row actions from the shared
// eligibility matrix (components/backups/backupArtifact) and hits the tenant
// resource paths. These cover the behaviors the consolidation changed:
//   - a running job now hides every destructive action (was: Delete always shown)
//   - a restore-history row shows "Remove" only
//   - Delete/Remove call DELETE /me/backups/:id  (AC2 — correct adapter path)
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MyProfileBackupCard } from "./MyProfileBackupCard";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

// The row Delete pops a feedback.modal.confirm before firing; auto-confirm it so
// the click reaches apiClient.delete. (RowActions imports this same module.)
vi.mock("../../lib/feedback", () => ({
  feedback: {
    modal: { confirm: (o: { onOk?: () => void }) => o.onOk?.() },
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  },
}));

import { apiClient } from "../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

function row(id: string, status: string, kind = "account_backup") {
  return { id, kind, status, bytes_total: 100, bytes_added: 100, created_at: "2026-08-24T00:00:00Z" };
}

function mockBackups(rows: unknown[]) {
  mocked.get.mockReset().mockImplementation((url: string) => {
    if (url.startsWith("/me/backups/exclusions")) return Promise.resolve({ data: { patterns: "" } });
    if (url.startsWith("/me/backups/destinations")) return Promise.resolve({ data: { data: [], allow_local: false } });
    if (url.startsWith("/me/backups")) return Promise.resolve({ data: { data: rows } });
    return Promise.resolve({ data: { data: [] } });
  });
}

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <MyProfileBackupCard />
      </App>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mocked.put.mockReset().mockResolvedValue({ data: {} });
  mocked.post.mockReset().mockResolvedValue({ data: {} });
  mocked.delete.mockReset().mockResolvedValue({ data: { status: "ok" } });
});

describe("MyProfileBackupCard row actions (JAB-332)", () => {
  it("hides every action while a backup is running", async () => {
    mockBackups([row("r1", "running")]);
    renderCard();
    await screen.findByText(/running/i);
    // canDelete is false while running (cancel-first policy); the tenant has no
    // Cancel, so the row shows no destructive control until it settles.
    expect(screen.queryByText("Download")).toBeNull();
    expect(screen.queryByText("Restore")).toBeNull();
    expect(screen.queryByText("Delete")).toBeNull();
    expect(screen.queryByText("Remove")).toBeNull();
  });

  it("shows Remove (not Download/Restore) for a restore-history row", async () => {
    mockBackups([row("h1", "succeeded", "account_restore")]);
    renderCard();
    await waitFor(() => expect(screen.getByText("Remove")).toBeTruthy());
    expect(screen.queryByText("Download")).toBeNull();
    expect(screen.queryByText("Restore")).toBeNull();
  });

  it("Remove on a restore row calls DELETE /me/backups/:id", async () => {
    mockBackups([row("h2", "succeeded", "account_restore")]);
    renderCard();
    fireEvent.click(await screen.findByText("Remove"));
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/me/backups/h2"));
  });

  it("still offers Restore on a succeeded row that omits kind", async () => {
    // A row without an explicit kind is an account backup (matches the Type
    // column default); Restore must not silently vanish. It sits in the overflow
    // menu behind Download, so open the menu to find it.
    mockBackups([{ id: "k1", status: "succeeded", bytes_total: 1, bytes_added: 1, created_at: "2026-08-24T00:00:00Z" }]);
    renderCard();
    fireEvent.click(await screen.findByLabelText("More actions"));
    await waitFor(() => expect(screen.getByText("Restore")).toBeTruthy());
  });

  it("Delete on a failed backup calls DELETE /me/backups/:id", async () => {
    // A failed backup: Download + Restore hidden, so Delete is the primary
    // (first visible) button — click it directly.
    mockBackups([row("f1", "failed")]);
    renderCard();
    fireEvent.click(await screen.findByText("Delete"));
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/me/backups/f1"));
  });
});
