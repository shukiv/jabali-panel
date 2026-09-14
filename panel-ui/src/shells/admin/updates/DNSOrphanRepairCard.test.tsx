// DNSOrphanRepairCard.test.tsx — GH #1620. Locks the wire contract for the
// Repair Center PowerDNS orphan-record action: the Scan button is a manual
// dry-run GET, the counts/names it returns render, and Delete POSTs to the
// prune route (never the scan route). Only apiClient is mocked; TanStack + AntD
// run for real.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App } from "antd";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));

import { apiClient } from "../../../apiClient";
import { DNSOrphanRepairCard } from "./DNSOrphanRepairCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPost = apiClient.post as ReturnType<typeof vi.fn>;

const SCAN_RESULT = {
  applied: false,
  counts: { records: 2, cryptokeys: 1 },
  deleted: {},
  names: ["gone.example.", "old.example."],
};

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <DNSOrphanRepairCard />
      </App>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("DNSOrphanRepairCard", () => {
  it("scan is a manual dry-run GET and renders the returned counts + names", async () => {
    mockGet.mockResolvedValue({ data: SCAN_RESULT });
    renderCard();

    // enabled:false — nothing fetched until the admin clicks Scan.
    expect(mockGet).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: /Delete records?/ })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: /Scan for orphan records/ }));

    await waitFor(() =>
      expect(mockGet).toHaveBeenCalledWith("/admin/updates/repair/dns/orphans"),
    );
    await screen.findByText(
      /3 orphan row\(s\) across 2 distinct record name\(s\)\./,
    );
    expect(screen.getByText("gone.example.")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Delete 3 records/ }),
    ).toBeEnabled();
  });

  it("delete POSTs to the prune route, never the scan route", async () => {
    mockGet.mockResolvedValue({ data: SCAN_RESULT });
    mockPost.mockResolvedValue({
      data: { ...SCAN_RESULT, applied: true, deleted: { records: 2, cryptokeys: 1 } },
    });
    renderCard();

    fireEvent.click(screen.getByRole("button", { name: /Scan for orphan records/ }));
    const del = await screen.findByRole("button", { name: /Delete 3 records/ });

    fireEvent.click(del);
    // Popconfirm danger confirm.
    fireEvent.click(await screen.findByText("Delete permanently"));

    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    expect(mockPost).toHaveBeenCalledWith(
      "/admin/updates/repair/dns/orphans/prune",
    );
  });
});
