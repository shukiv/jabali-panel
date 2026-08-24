// JAB-327: the tenant backup card must expose Download for a `partial` backup
// (it has a valid snapshot), mirroring the admin page + the backend gate — not
// only for `succeeded`. Restore stays succeeded-only.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MyProfileBackupCard } from "./MyProfileBackupCard";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../apiClient";

const mocked = apiClient as unknown as { get: ReturnType<typeof vi.fn>; put: ReturnType<typeof vi.fn>; post: ReturnType<typeof vi.fn>; delete: ReturnType<typeof vi.fn> };

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
});

describe("MyProfileBackupCard download visibility (JAB-327)", () => {
  it("shows Download for a partial backup", async () => {
    mockBackups([row("b1", "partial")]);
    renderCard();
    // RowActions renders the primary (first) action as a visible text button.
    await waitFor(() => expect(screen.getAllByText("Download").length).toBeGreaterThan(0));
  });

  it("does NOT show Download for a failed backup", async () => {
    mockBackups([row("b2", "failed")]);
    renderCard();
    // Let the table render the row, then assert no Download action.
    await screen.findByText(/failed/i);
    expect(screen.queryByText("Download")).toBeNull();
  });

  it("shows Download for a succeeded backup", async () => {
    mockBackups([row("b3", "succeeded")]);
    renderCard();
    await waitFor(() => expect(screen.getAllByText("Download").length).toBeGreaterThan(0));
  });
});
