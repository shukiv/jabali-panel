// SSLTab.test — GH #1543. The tenant per-domain SSL tab: it reads the
// owner-scoped GET /domains/:id/ssl and gates View Certificate to an issued
// cert. It offers no Renew / Retry (those endpoints are admin-only and only
// ever answered a tenant with a 403) and no certificate-mode switch.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
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
  it("issued: shows View Certificate and says renewal is automatic, with no Renew or Retry", async () => {
    get.mockResolvedValue({ data: { ssl: { status: "issued", issued_at: inDays(-5), expires_at: inDays(80) } } });
    renderTab();
    expect(await screen.findByRole("button", { name: /View Certificate/ })).toBeInTheDocument();
    expect(screen.getByText(/renews automatically/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Renew/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retry/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /View Certificate/ }));
    expect(await screen.findByText("cert-modal:d1")).toBeInTheDocument();
    expect(post).not.toHaveBeenCalled();
  });

  it("failed: shows the error and says issuance is retried automatically, with no Retry", async () => {
    get.mockResolvedValue({ data: { ssl: { status: "failed", last_error: "DNS not resolving" } } });
    renderTab();
    expect(await screen.findByText("DNS not resolving")).toBeInTheDocument();
    expect(screen.getByText(/retries issuance automatically/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retry/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Renew/ })).not.toBeInTheDocument();
  });

  it("pending_acme_retry: no Retry either", async () => {
    get.mockResolvedValue({ data: { ssl: { status: "pending_acme_retry", last_error: "waiting on DNS" } } });
    renderTab();
    expect(await screen.findByText(/retries issuance automatically/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retry/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Renew/ })).not.toBeInTheDocument();
  });

  it("self_signed: explains the self-signed cert and offers no actions (inspect 409s for non-issued)", async () => {
    get.mockResolvedValue({ data: { ssl: { status: "self_signed" } } });
    renderTab();
    expect(await screen.findByText(/served with a self-signed certificate/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /View Certificate/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Renew/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retry/ })).not.toBeInTheDocument();
  });

  it("no cert (404): renders without any action", async () => {
    get.mockRejectedValue({ response: { status: 404 } });
    renderTab();
    expect(await screen.findByText(/SSL is managed by the server administrator/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /View Certificate/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Renew|Retry/ })).not.toBeInTheDocument();
  });
});
