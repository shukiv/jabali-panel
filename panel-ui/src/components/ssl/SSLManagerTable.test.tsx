// SSLManagerTable.test — Renew and Retry call admin-only endpoints
// (POST /domains/:id/ssl/renew, /ssl/retry), so the tenant SSL Manager turns
// them off (adminActions={false}) instead of showing buttons that fail with a
// 403. The admin page keeps them.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());
vi.mock("../../apiClient", () => ({ apiClient: { get, post: vi.fn(), delete: vi.fn() } }));

import { SSLManagerTable } from "./SSLManagerTable";

const rows = [
  { id: "c1", domain_id: "d1", domain_name: "issued.tld", status: "issued", ssl_mode: "le", expires_at: new Date(Date.now() + 60 * 86_400_000).toISOString() },
  { id: "c2", domain_id: "d2", domain_name: "failed.tld", status: "failed", ssl_mode: "le", last_error: "DNS" },
];

function renderTable(adminActions?: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <SSLManagerTable endpoint="/ssl-certificates" showOwner={false} adminActions={adminActions} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  get.mockReset();
  get.mockResolvedValue({ data: { items: rows } });
});

describe("SSLManagerTable admin-only actions", () => {
  it("tenant (adminActions=false): no Renew or Retry on any row", async () => {
    renderTable(false);
    expect(await screen.findByText("issued.tld")).toBeInTheDocument();
    expect(screen.getByText("failed.tld")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Renew/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retry/ })).not.toBeInTheDocument();
  });

  it("admin (default): Renew on the issued row and Retry on the failed one", async () => {
    renderTable();
    expect(await screen.findByRole("button", { name: /Renew/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Retry now/ })).toBeInTheDocument();
  });
});
