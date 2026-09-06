// JAB-332: the admin backups page drives its per-job actions from the same
// shared eligibility matrix as the tenant card, and hits the /admin resource
// paths. These cover the admin side of the consolidation:
//   - the run grouping (RunRow → "Expand to manage") is unchanged            (AC3)
//   - a standalone job's Delete is gated by the shared canDelete (running hidden)
//   - Delete calls DELETE /admin/backups/:id                                 (AC2)
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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

function job(id: string, status: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    user_id: "u1",
    kind: "account_backup",
    content: "full",
    status,
    systemd_unit: `backup-${id}.service`,
    snapshot_id: `snap-${id}`,
    bytes_added: 100,
    bytes_total: 100,
    created_at: "2026-08-24T00:00:00Z",
    ...extra,
  };
}

const RUN = {
  run_id: "run1",
  schedule_id: "sched1",
  has_accounts: true,
  accounts: 2,
  kind: "account_backup",
  content: "full",
  total: 2,
  succeeded: 2,
  failed: 0,
  running: 0,
  queued: 0,
  cancelled: 0,
  partial: 0,
  bytes_added: 200,
  bytes_total: 200,
  started_at: "2026-08-25T00:00:00Z",
  latest_updated: "2026-08-25T00:00:00Z",
};

function mockData(manual: unknown[]) {
  mocked.get.mockReset().mockImplementation((url: string) => {
    if (url.startsWith("/admin/backup-runs")) {
      return Promise.resolve({
        data: { data: [RUN], manual, total: 1, manual_total: manual.length },
      });
    }
    if (url.startsWith("/users")) {
      return Promise.resolve({ data: { data: [{ id: "u1", username: "alice", email: "a@x" }], total: 1 } });
    }
    if (url.startsWith("/admin/backup-destinations")) {
      return Promise.resolve({ data: { data: [], total: 0 } });
    }
    return Promise.resolve({ data: { data: [] } });
  });
}

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

beforeEach(() => {
  mocked.put.mockReset().mockResolvedValue({ data: {} });
  mocked.post.mockReset().mockResolvedValue({ data: {} });
  mocked.delete.mockReset().mockResolvedValue({ data: { status: "ok" } });
});

describe("AdminBackupsPage per-job actions (JAB-332)", () => {
  it("keeps the run grouping (RunRow is expandable to manage)", async () => {
    mockData([job("m1", "succeeded")]);
    renderPage();
    // AC3: the scheduler run still rolls up under one expandable parent row.
    expect(await screen.findByText("Expand to manage")).toBeTruthy();
  });

  it("shows Delete for a finished standalone job but hides it while running", async () => {
    mockData([job("m1", "succeeded"), job("m2", "running")]);
    renderPage();
    await screen.findByText("Expand to manage");
    // m1 (succeeded) shows a Delete; m2 (running) hides it → exactly one exact
    // "Delete" button in the collapsed table.
    await waitFor(() => expect(screen.getAllByText("Delete")).toHaveLength(1));
  });

  it("Delete on a standalone job calls DELETE /admin/backups/:id", async () => {
    mockData([job("m1", "succeeded")]);
    renderPage();
    fireEvent.click(await screen.findByText("Delete"));
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/admin/backups/m1"));
  });
});
