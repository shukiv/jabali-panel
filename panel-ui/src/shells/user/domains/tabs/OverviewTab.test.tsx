// OverviewTab.test — GH #1543. The SSL row on the tenant Web Domain Overview:
// the badge, and — only for an issued cert — the expiry and a View Certificate
// action, plus an always-present "SSL settings" link to the SSL tab.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const navigate = vi.hoisted(() => vi.fn());
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => navigate };
});
vi.mock("../../../../apiClient", () => ({ apiClient: { patch: vi.fn().mockResolvedValue({}) } }));
vi.mock("../../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));
// Isolate from the cert-fetch modal — assert the wiring, not the network.
vi.mock("../../../../components/ssl/SSLCertViewModal", () => ({
  SSLCertViewModal: ({ domainId }: { domainId: string | null }) =>
    domainId ? <div>cert-modal:{domainId}</div> : null,
}));

import { OverviewTab } from "./OverviewTab";
import type { Domain } from "../../../../components/domains/types";

const base: Domain = {
  id: "d1",
  name: "site.tld",
  doc_root: "/home/alice/site.tld/public_html",
  is_enabled: true,
} as Domain;

const inDays = (n: number) => new Date(Date.now() + n * 86_400_000).toISOString();

function renderTab(domain: Domain) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <OverviewTab domain={domain} />
    </QueryClientProvider>,
  );
}

beforeEach(() => navigate.mockReset());

describe("OverviewTab SSL row (GH #1543)", () => {
  it("shows expiry and View Certificate for an issued cert, and opens the cert modal", () => {
    renderTab({
      ...base,
      ssl_state: "active_le",
      ssl: { status: "issued", issuer: "Let's Encrypt", expires_at: inDays(60) },
    } as Domain);
    expect(screen.getByText(/expires in 60 days/)).toBeInTheDocument();
    const view = screen.getByRole("button", { name: /View Certificate/ });
    fireEvent.click(view);
    expect(screen.getByText("cert-modal:d1")).toBeInTheDocument();
  });

  it("hides View Certificate and expiry for a not-yet-issued cert but keeps the SSL settings link", () => {
    renderTab({
      ...base,
      ssl_state: "pending",
      // A parked cert's nested expires_at is the self-signed fallback date — it
      // must NOT surface as the certificate's expiry.
      ssl: { status: "pending_acme_retry", expires_at: inDays(300) },
    } as Domain);
    expect(screen.queryByText(/expires in/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /View Certificate/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "SSL settings" })).toBeInTheDocument();
  });

  it("navigates to the SSL tab when SSL settings is clicked", () => {
    renderTab({ ...base, ssl_state: "off" } as Domain);
    fireEvent.click(screen.getByRole("button", { name: "SSL settings" }));
    expect(navigate).toHaveBeenCalledWith("/jabali-panel/domains/d1/ssl");
  });
});

describe("OverviewTab subdomain delegation toggle (GH #1812)", () => {
  it("renders the delegation switch reflecting the stored flag", () => {
    renderTab({ ...base, allow_subdomain_delegation: true } as Domain);
    const sw = screen.getByRole("switch", { name: "Allow subdomains by other accounts" });
    expect(sw.getAttribute("aria-checked")).toBe("true");
  });

  it("PATCHes allow_subdomain_delegation when the owner turns it on", async () => {
    const { apiClient } = await import("../../../../apiClient");
    const patch = vi.mocked(apiClient.patch);
    patch.mockClear();
    renderTab({ ...base, allow_subdomain_delegation: false } as Domain);
    fireEvent.click(screen.getByRole("switch", { name: "Allow subdomains by other accounts" }));
    await vi.waitFor(() =>
      expect(patch).toHaveBeenCalledWith("/domains/d1", { allow_subdomain_delegation: true }),
    );
  });

  it("says turning it off does not remove subdomains other accounts already created", () => {
    renderTab({ ...base } as Domain);
    expect(screen.getByText(/Turning this off stops new ones/)).toBeInTheDocument();
  });
});
