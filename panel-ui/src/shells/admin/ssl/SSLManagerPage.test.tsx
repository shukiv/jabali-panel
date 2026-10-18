// SSLManagerPage.test.tsx — GH #2166. The panel's own certs are managed on
// SSL Manager's Panel SSL tab, which replaced the compact SYSTEM band above
// the tabs. The tab is kept in ?tab= so other pages can link to it, and a
// summary tile, which filters the domains table, opens that table.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());
vi.mock("../../../apiClient", () => ({ apiClient: { get, post: vi.fn() } }));
vi.mock("../../../components/ssl/SSLManagerTable", () => ({
  SSLManagerTable: ({ statusFilter }: { statusFilter: string }) => <div>domain table: {statusFilter}</div>,
}));
vi.mock("../../../components/ssl/SharedCertificatesCard", () => ({
  SharedCertificatesCard: () => <div>shared certificates</div>,
}));

import { SSLManagerPage } from "./SSLManagerPage";

function cert(kind: "hostname" | "mail") {
  return {
    kind,
    id: 1,
    hostname: kind === "mail" ? "mail.panel.example.com" : "panel.example.com",
    status: "self_signed",
    cert_pem_path: "/etc/jabali/tls/panel.crt",
    attempt_count: 0,
    staging: false,
    use_le: false,
    updated_at: "2026-10-18T00:00:00Z",
    routable: true,
  };
}

let search = "";
function Probe() {
  search = useLocation().search;
  return null;
}

function renderAt(url: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter initialEntries={[url]}>
      <QueryClientProvider client={qc}>
        <App>
          <SSLManagerPage />
          <Probe />
        </App>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

const selectedTab = () => document.querySelector(".ant-tabs-tab-active")?.textContent;

beforeEach(() => {
  get.mockReset();
  get.mockImplementation(async (url: string) => {
    if (url === "/admin/panel-certificate") return { data: { certs: [cert("hostname"), cert("mail")] } };
    if (url === "/admin/certificates/shared") return { data: { data: [] } };
    return { data: { items: [] } };
  });
});
afterEach(() => cleanup());

describe("SSL Manager → Panel SSL tab (GH #2166)", () => {
  it("?tab=panel opens Panel SSL, and the SYSTEM band is gone", async () => {
    renderAt("/jabali-admin/ssl?tab=panel");
    expect(await screen.findByText("Use Let's Encrypt for the panel")).toBeInTheDocument();
    expect(selectedTab()).toBe("Panel SSL");
    expect(screen.queryByText("SYSTEM")).not.toBeInTheDocument();
  });

  it("opens on the domains table, and the Panel SSL tab is kept in the URL", async () => {
    renderAt("/jabali-admin/ssl");
    expect(await screen.findByText("domain table: all")).toBeInTheDocument();
    expect(selectedTab()).toBe("Domain Certificates");
    // Panel certs are only on their tab now.
    expect(screen.queryByText("Use Let's Encrypt for the panel")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Panel SSL" }));
    expect(await screen.findByText("Use Let's Encrypt for the panel")).toBeInTheDocument();
    expect(search).toBe("?tab=panel");
  });

  it("a summary tile filters the domains table and opens it", async () => {
    renderAt("/jabali-admin/ssl?tab=panel");
    await screen.findByText("Use Let's Encrypt for the panel");
    fireEvent.click(screen.getByRole("button", { name: /Issued/ }));
    expect(await screen.findByText("domain table: issued")).toBeInTheDocument();
    await waitFor(() => expect(selectedTab()).toBe("Domain Certificates"));
    expect(search).toBe("");
  });
});
