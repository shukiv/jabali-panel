// SSLTab.test — GH #1543. The tenant per-domain SSL tab: it reads the
// owner-scoped GET /domains/:id/ssl, gates View Certificate / Renew to an
// issued cert and Retry to a failed / parked one, and POSTs to the tenant
// renew/retry endpoints. It never offers a certificate-mode switch (admin-only).
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());
const post = vi.hoisted(() => vi.fn());
vi.mock("../../../../apiClient", () => ({ apiClient: { get, post } }));
vi.mock("../../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn(), info: vi.fn() } },
}));
vi.mock("../../../../components/ssl/SSLCertViewModal", () => ({
  SSLCertViewModal: ({ domainId }: { domainId: string | null }) =>
    domainId ? <div>cert-modal:{domainId}</div> : null,
}));

import { SSLTab } from "./SSLTab";
import type { Domain } from "../../../../components/domains/types";

const domain = { id: "d1", name: "site.tld", ssl_state: "active_le", ssl_mode: "le" } as Domain;
const inDays = (n: number) => new Date(Date.now() + n * 86_400_000).toISOString();

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <SSLTab domain={domain} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  post.mockResolvedValue({});
});

describe("SSLTab (GH #1543)", () => {
  it("issued: shows View Certificate + Renew, hides Retry, and renews via the tenant endpoint", async () => {
    get.mockResolvedValue({ data: { ssl: { status: "issued", issued_at: inDays(-5), expires_at: inDays(80) } } });
    renderTab();
    expect(await screen.findByRole("button", { name: /View Certificate/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retry now/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Renew now/ }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/domains/d1/ssl/renew"));
  });

  it("failed: shows the error alert + Retry, hides Renew, and retries via the tenant endpoint", async () => {
    get.mockResolvedValue({ data: { ssl: { status: "failed", last_error: "DNS not resolving" } } });
    renderTab();
    expect(await screen.findByText("DNS not resolving")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Renew now/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Retry now/ }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/domains/d1/ssl/retry"));
  });

  it("pending_acme_retry: offers a manual Retry", async () => {
    get.mockResolvedValue({ data: { ssl: { status: "pending_acme_retry", last_error: "waiting on DNS" } } });
    renderTab();
    expect(await screen.findByRole("button", { name: /Retry now/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Renew now/ })).not.toBeInTheDocument();
  });

  it("self_signed: explains the self-signed cert and offers no actions (inspect 409s for non-issued)", async () => {
    get.mockResolvedValue({ data: { ssl: { status: "self_signed" } } });
    renderTab();
    expect(await screen.findByText(/served with a self-signed certificate/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /View Certificate/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Renew now/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retry now/ })).not.toBeInTheDocument();
  });

  it("no cert (404): renders without View/Renew/Retry actions", async () => {
    get.mockRejectedValue({ response: { status: 404 } });
    renderTab();
    // The read settles to 'no certificate' — none of the action buttons appear.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: /Renew now/ })).not.toBeInTheDocument(),
    );
    expect(screen.queryByRole("button", { name: /Retry now/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /View Certificate/ })).not.toBeInTheDocument();
  });
});
