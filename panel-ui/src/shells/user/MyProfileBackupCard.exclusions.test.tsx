// MyProfileBackupCard.exclusions.test.tsx — GH #454 backup exclusions editor.
//
// The tenant edits ~/.backupignore via the card: it seeds a textarea from
// GET /me/backups/exclusions and saves with PUT. Only apiClient is mocked;
// the real AntD card renders.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MyProfileBackupCard } from "./MyProfileBackupCard";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

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
  mocked.get.mockReset().mockImplementation((url: string) => {
    if (url.startsWith("/me/backups/exclusions")) {
      return Promise.resolve({ data: { patterns: "cache/\nnode_modules/\n" } });
    }
    if (url.startsWith("/me/backups/destinations")) {
      return Promise.resolve({ data: { data: [], allow_local: false } });
    }
    return Promise.resolve({ data: { data: [] } });
  });
  mocked.put.mockReset().mockResolvedValue({ data: {} });
});

describe("MyProfileBackupCard exclusions (GH #454)", () => {
  it("seeds the exclusions textarea from GET and saves edits via PUT", async () => {
    renderCard();

    // Seeded from GET /me/backups/exclusions.
    const ta = await screen.findByDisplayValue(/cache\//);
    expect(ta).toBeInTheDocument();

    // Edit + save.
    fireEvent.change(ta, { target: { value: "logs/\n*.tmp\n" } });
    fireEvent.click(screen.getByRole("button", { name: /save exclusions/i }));

    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/me/backups/exclusions", {
        patterns: "logs/\n*.tmp\n",
      }),
    );
  });
});
