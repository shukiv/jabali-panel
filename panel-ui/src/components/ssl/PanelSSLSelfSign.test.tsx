// PanelSSLSelfSign.test.tsx — GH #2166. With "Use Let's Encrypt for the
// panel" off, the hostname cert's action regenerates the self-signed cert
// instead of asking Let's Encrypt, on SSL Manager → Panel SSL, where the
// panel's certs are managed. The mail cert stays a Let's Encrypt action.
// Server Settings → Panel SSL only shows their status and links there.
// apiClient and the toasts are mocked; the real AntD Popconfirm renders.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));
vi.mock("../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn(), success: vi.fn() } },
}));

import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback";
import { PanelSSLCard } from "../../shells/admin/settings/PanelSSLCard";
import { setPanelClock } from "../../utils/datetime";
import { PanelSSLTab } from "./PanelSSLTab";
import { panelCertIssueMessage } from "./panelCertStatus";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};
const toastOK = feedback.message.success as unknown as ReturnType<typeof vi.fn>;

function cert(kind: "hostname" | "mail", useLE: boolean) {
  return {
    kind,
    id: 1,
    hostname: kind === "mail" ? "mail.panel.example.com" : "panel.example.com",
    status: "self_signed",
    cert_pem_path: kind === "mail" ? "/etc/jabali/tls/panel-mail.crt" : "/etc/jabali/tls/panel.crt",
    attempt_count: 0,
    staging: false,
    use_le: useLE,
    updated_at: "2026-10-17T00:00:00Z",
    routable: false,
    routable_reason: "dns points elsewhere",
  };
}

function renderWith(node: React.ReactNode, useLE: boolean) {
  mocked.get.mockResolvedValue({ status: 200, data: { certs: [cert("hostname", useLE), cert("mail", useLE)] } });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        <App>{node}</App>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  setPanelClock("UTC", "DD/MM/YY HH:mm");
});
afterEach(() => cleanup());

describe("panelCertIssueMessage (GH #2166)", () => {
  it("is the server's words, with the current cert's expiry in the admin's format", () => {
    expect(
      panelCertIssueMessage("hostname", {
        self_signed: true,
        regenerating: false,
        detail: "The panel already serves a self-signed certificate for panel.example.com.",
        expires_at: "2036-10-17T06:00:00Z",
      }),
    ).toBe("The panel already serves a self-signed certificate for panel.example.com. Valid until 17/10/36.");
  });

  it("has no date while regenerating", () => {
    expect(
      panelCertIssueMessage("hostname", { self_signed: true, regenerating: true, detail: "Regenerating the self-signed certificate." }),
    ).toBe("Regenerating the self-signed certificate.");
  });

  it("is unchanged for a Let's Encrypt issue", () => {
    expect(panelCertIssueMessage("mail", cert("mail", true) as never)).toBe("mail cert: issued");
  });
});

for (const [name, node] of [["SSL Manager → Panel SSL", <PanelSSLTab key="tab" />]] as const) {
  describe(`${name} (GH #2166)`, () => {
    it("with Let's Encrypt off, the hostname regenerates the self-signed cert", async () => {
      mocked.post.mockResolvedValue({
        status: 202,
        data: { self_signed: true, regenerating: true, detail: "Regenerating the self-signed certificate for panel.example.com." },
      });
      renderWith(node, false);

      fireEvent.click(await screen.findByRole("button", { name: /Regenerate self-signed/ }));
      expect(await screen.findByText("Regenerate the self-signed certificate?")).toBeInTheDocument();
      expect(screen.getByText(/your browser asks you to accept the new certificate/)).toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: "OK" }));

      await waitFor(() => expect(mocked.post).toHaveBeenCalledWith("/admin/panel-certificate/issue?kind=hostname"));
      await waitFor(() =>
        expect(toastOK).toHaveBeenCalledWith("Regenerating the self-signed certificate for panel.example.com.", 10),
      );
      // The mail cert is still a Let's Encrypt action.
      expect(screen.getAllByRole("button", { name: /Issue \/ retry/ })).toHaveLength(1);
    });

    it("with Let's Encrypt on, both certs are Let's Encrypt actions", async () => {
      renderWith(node, true);
      expect(await screen.findAllByRole("button", { name: /Issue \/ retry/ })).toHaveLength(2);
      expect(screen.queryByRole("button", { name: /Regenerate self-signed/ })).not.toBeInTheDocument();
    });
  });
}

// The Settings card keeps the certs in sight and sends the admin to the tab:
// no switches or actions of its own, so the two can never disagree.
describe("Server Settings → Panel SSL (GH #2166)", () => {
  it("shows each cert's status and links to SSL Manager → Panel SSL", async () => {
    renderWith(<PanelSSLCard />, false);
    expect(await screen.findByText("panel.example.com")).toBeInTheDocument();
    expect(screen.getByText("mail.panel.example.com")).toBeInTheDocument();
    expect(screen.getAllByText("Self-signed")).toHaveLength(2);
    const links = screen.getAllByRole("link");
    expect(links.length).toBeGreaterThan(0);
    for (const l of links) expect(l).toHaveAttribute("href", "/jabali-admin/ssl?tab=panel");
    expect(screen.getByRole("link", { name: "SSL Manager → Panel SSL" })).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
  });
});
