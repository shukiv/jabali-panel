// GH #2089: the Admin Backups list says whose backup each row is and where it
// is stored, instead of a truncated ID and "—":
//   - a User column replaces the ID column: the account's username, "System",
//     or the run's scope ("system + 2 accounts") when a run spans accounts
//   - a Destination column: "<name> (<KIND>)", "Local" for the implicit local
//     repo, "Uploaded file" for a restore from an upload, "Deleted
//     destination" for one that no longer exists, "Several" for a mixed run
//   - the expanded row shows the full job ID, and no User column under a
//     single job whose row already names the account
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

const LONG_ID = "01K7ABCDEFGHJKMNPQRSTVWXYZ";

function job(id: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    user_id: "u1",
    kind: "account_backup",
    content: "full",
    status: "succeeded",
    systemd_unit: `backup-${id}.service`,
    snapshot_id: `snap-${id}`,
    bytes_added: 100,
    bytes_total: 100,
    created_at: "2026-08-24T00:00:00Z",
    ...extra,
  };
}

function run(id: string, extra: Record<string, unknown> = {}) {
  return {
    run_id: id,
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
    destinations: 1,
    started_at: "2026-08-25T00:00:00Z",
    latest_updated: "2026-08-25T00:00:00Z",
    ...extra,
  };
}

function mockData(runs: unknown[], manual: unknown[]) {
  mocked.get.mockReset().mockImplementation((url: string) => {
    if (url.startsWith("/admin/backup-runs")) {
      return Promise.resolve({
        data: { data: runs, manual, total: runs.length, manual_total: manual.length },
      });
    }
    if (url.startsWith("/users")) {
      return Promise.resolve({
        data: {
          data: [
            { id: "u1", username: "alice", email: "a@x" },
            { id: "u2", username: "bob", email: "b@x" },
          ],
          total: 2,
        },
      });
    }
    if (url.startsWith("/admin/backup-destinations")) {
      return Promise.resolve({ data: { data: [{ id: "d1", name: "Offsite", kind: "sftp" }], total: 1 } });
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

// The collapsed table row holding the given text.
async function rowWith(text: string): Promise<HTMLElement> {
  const cell = await screen.findByText(text);
  const row = cell.closest("tr");
  if (!row) throw new Error(`no row for ${text}`);
  return row as HTMLElement;
}

beforeEach(() => {
  mocked.post.mockReset().mockResolvedValue({ data: {} });
  mocked.delete.mockReset().mockResolvedValue({ data: {} });
});

describe("AdminBackupsPage names the account and destination (GH #2089)", () => {
  it("has a User column instead of the truncated ID column", async () => {
    mockData([], [job(LONG_ID)]);
    renderPage();
    await screen.findByText("alice");
    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent ?? "");
    expect(headers).toContain("User");
    expect(headers).toContain("Destination");
    expect(headers).not.toContain("ID");
    expect(screen.queryByText(`${LONG_ID.slice(0, 8)}…`)).toBeNull();
  });

  it("names the account, System, or the run's scope", async () => {
    mockData(
      [
        run("run-one", { user_id: "u2", accounts: 1, total: 1 }),
        run("run-full", { kind: "system_backup", accounts: 2, total: 3 }),
        run("run-sys", { kind: "system_backup", user_id: "system", has_accounts: false, accounts: 0, total: 1 }),
      ],
      [job("m1"), job("m2", { user_id: "system", kind: "system_backup" })],
    );
    renderPage();
    expect(await screen.findByText("alice")).toBeTruthy();
    expect(screen.getByText("bob")).toBeTruthy();
    expect(screen.getByText("system + 2 accounts")).toBeTruthy();
    expect(screen.getAllByText("System")).toHaveLength(2);
  });

  it("says where each backup is stored", async () => {
    mockData(
      [run("run-mixed", { destinations: 2 })],
      [
        job("m-remote", { destination_id: "d1" }),
        job("m-local", { user_id: "u2", created_at: "2026-08-23T00:00:00Z" }),
        job("m-upload", { kind: "account_restore", created_at: "2026-08-22T00:00:00Z" }),
        job("m-gone", { destination_id: "dgone", created_at: "2026-08-21T00:00:00Z" }),
      ],
    );
    renderPage();
    expect(await screen.findByText("Offsite (SFTP)")).toBeTruthy();
    expect(screen.getByText("Local")).toBeTruthy();
    expect(screen.getByText("Uploaded file")).toBeTruthy();
    expect(screen.getByText("Deleted destination")).toBeTruthy();
    expect(screen.getByText("Several")).toBeTruthy();
  });

  it("shows the full job ID when a job is expanded, without repeating the account", async () => {
    mockData([], [job(LONG_ID, { destination_id: "d1" })]);
    renderPage();
    const row = await rowWith("Offsite (SFTP)");
    fireEvent.click(within(row).getByRole("button", { name: /expand row/i }));
    await waitFor(() => expect(screen.getByText(LONG_ID)).toBeTruthy());
    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent ?? "");
    expect(headers.filter((h) => h === "User")).toHaveLength(1);
    expect(headers).toContain("Job ID");
    expect(headers).toContain("Created");
  });
});
