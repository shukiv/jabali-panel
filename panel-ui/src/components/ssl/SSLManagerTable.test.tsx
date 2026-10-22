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

// GH #2200: a domain whose owner has not proven the name yet gets a
// self-signed placeholder and no Let's Encrypt attempt. The row says so, and
// offers no Retry, which the server refuses for it (409 domain_ownership_pending).
describe("SSLManagerTable ownership-pending rows", () => {
  it("marks the row and offers no Retry", async () => {
    get.mockResolvedValue({
      data: {
        items: [
          { id: "c3", domain_id: "d3", domain_name: "unproven.tld", status: "self_signed", ssl_mode: "le", ownership_pending: true },
          {
            id: "c4", domain_id: "d4", domain_name: "parked.tld", status: "pending_acme_retry", ssl_mode: "le",
            ownership_pending: true, next_retry_at: new Date(Date.now() - 60_000).toISOString(),
          },
        ],
      },
    });
    renderTable();
    expect(await screen.findByText("unproven.tld")).toBeInTheDocument();
    expect(screen.getAllByText("Ownership pending")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: /Retry now/ })).not.toBeInTheDocument();
  });

  it("a verified self-signed row has no ownership mark", async () => {
    get.mockResolvedValue({
      data: { items: [{ id: "c5", domain_id: "d5", domain_name: "selfmode.tld", status: "self_signed", ssl_mode: "self" }] },
    });
    renderTable();
    expect(await screen.findByText("selfmode.tld")).toBeInTheDocument();
    expect(screen.queryByText("Ownership pending")).not.toBeInTheDocument();
  });
});
