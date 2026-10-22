// DomainRepairCard.test.tsx — GH #2237. The orphan-site delete reports what
// the prune did: a success only when every site went, and the sites still
// there, each with its reason, otherwise. Only apiClient is mocked; TanStack +
// AntD run for real.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App } from "antd";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));

import { apiClient } from "../../../apiClient";
import { DomainRepairCard } from "./DomainRepairCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPost = apiClient.post as ReturnType<typeof vi.fn>;

const SCAN = {
  orphans: ["gone.example", "stuck.example"],
  applied: false,
  deleted: [],
  failed: [],
};

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <DomainRepairCard />
      </App>
    </QueryClientProvider>,
  );
}

async function scanThenDelete() {
  fireEvent.click(screen.getByRole("button", { name: /Scan for orphans/ }));
  fireEvent.click(await screen.findByRole("button", { name: /Delete 2 orphans/ }));
  fireEvent.click(await screen.findByText("Delete permanently"));
  await waitFor(() =>
    expect(mockPost).toHaveBeenCalledWith("/admin/updates/repair/domain/orphans/prune"),
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockGet.mockImplementation((url: string) =>
    url === "/users"
      ? Promise.resolve({ data: { data: [] } })
      : Promise.resolve({ data: SCAN }),
  );
});

describe("DomainRepairCard orphan delete", () => {
  it("names each site the delete left, with its reason, and claims no success", async () => {
    mockPost.mockResolvedValue({
      data: {
        ...SCAN,
        applied: true,
        deleted: ["gone.example"],
        failed: [
          { site: "stuck.example", error: "still in /etc/nginx/sites-enabled after the delete" },
        ],
      },
    });
    renderCard();
    await scanThenDelete();

    expect(await screen.findAllByText("1 orphan site was not deleted")).not.toHaveLength(0);
    expect(
      screen.getByText("still in /etc/nginx/sites-enabled after the delete"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Deleted \d+ orphan site/)).toBeNull();
    expect(screen.queryByText("Orphan sites deleted")).toBeNull();
  });

  it("reports the count when every site went", async () => {
    mockPost.mockResolvedValue({
      data: { ...SCAN, applied: true, deleted: ["gone.example", "stuck.example"], failed: [] },
    });
    renderCard();
    await scanThenDelete();

    expect(await screen.findByText("Deleted 2 orphan sites")).toBeInTheDocument();
    expect(screen.queryByText(/not deleted/)).toBeNull();
  });
});
