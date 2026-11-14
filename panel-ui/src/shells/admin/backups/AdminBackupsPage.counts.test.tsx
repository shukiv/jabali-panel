// GH #2505: the Admin Backups page asks for the full page (page_size=1000)
// and its counters come from the server totals, not the loaded rows: the
// badge shows total + manual_total, and when the server holds more backups
// than the page loaded, one secondary line says how many of all are shown.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AdminBackupsPage } from "./AdminBackupsPage";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

vi.mock("../../../lib/feedback", () => ({
  feedback: {
    modal: { confirm: (o: { onOk?: () => void }) => o.onOk?.() },
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  },
}));

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (k: string) => k }) }));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

function run(i: number) {
  return {
    run_id: `run-${i}`,
    kind: "account_backup",
    content: "full",
    user_id: "u1",
    total: 1,
    succeeded: 1,
    failed: 0,
    running: 0,
    queued: 0,
    cancelled: 0,
    partial: 0,
    bytes_added: 100,
    bytes_total: 100,
    started_at: "2026-08-25T00:00:00Z",
    latest_updated: "2026-08-25T00:00:00Z",
  };
}

beforeEach(() => {
  mocked.get.mockReset().mockImplementation((url: string) => {
    if (url.startsWith("/admin/backup-runs")) {
      return Promise.resolve({
        data: {
          data: Array.from({ length: 25 }, (_, i) => run(i)),
          manual: [
            {
              id: "m1",
              user_id: "u1",
              kind: "account_backup",
              content: "full",
              status: "succeeded",
              systemd_unit: "backup-m1.service",
              snapshot_id: "snap-m1",
              bytes_added: 100,
              bytes_total: 100,
              created_at: "2026-08-24T00:00:00Z",
            },
          ],
          total: 70,
          manual_total: 1,
        },
      });
    }
    if (url.startsWith("/users")) {
      return Promise.resolve({
        data: { data: [{ id: "u1", username: "alice", email: "a@x" }], total: 1 },
      });
    }
    return Promise.resolve({ data: { data: [], total: 0 } });
  });
  mocked.post.mockReset().mockResolvedValue({ data: {} });
  mocked.delete.mockReset().mockResolvedValue({ data: {} });
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <App>
          <AdminBackupsPage />
        </App>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("AdminBackupsPage counters (GH #2505)", () => {
  it("requests the full page and counts every backup, not just the loaded rows", async () => {
    renderPage();
    // 25 runs loaded of 70, 1 manual of 1: the badge counts all 71 and the
    // secondary line names the 26 loaded rows.
    expect(await screen.findByText("Showing the newest 26 of 71 backups.")).toBeTruthy();
    // The Badge splits its count into per-digit spans; the full count is the
    // scroll-number's title.
    expect(screen.getByTitle("71")).toBeTruthy();
    expect(mocked.get).toHaveBeenCalledWith("/admin/backup-runs?page_size=1000");
  });
});
