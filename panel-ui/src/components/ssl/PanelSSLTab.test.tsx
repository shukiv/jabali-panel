// PanelSSLTab.test.tsx — GH #2166. SSL Manager → Panel SSL shows what each
// panel cert's file holds: type, issuer, names, validity in the admin's date
// format and the SHA-256 fingerprint, with a warning before expiry and the
// reason when there is no usable file.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));
vi.mock("../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn(), success: vi.fn() } },
}));

import { apiClient } from "../../apiClient";
import { setPanelClock } from "../../utils/datetime";
import { PanelSSLTab } from "./PanelSSLTab";

const get = (apiClient as unknown as { get: ReturnType<typeof vi.fn> }).get;

const FP = Array.from({ length: 32 }, (_, i) => (i + 16).toString(16).toUpperCase()).join(":");
const DAY = 24 * 3600 * 1000;

function cert(kind: "hostname" | "mail", over: Record<string, unknown> = {}) {
  return {
    kind,
    id: 1,
    hostname: kind === "mail" ? "mail.panel.example.com" : "panel.example.com",
    status: "self_signed",
    cert_pem_path: kind === "mail" ? "/etc/jabali/tls/panel-mail.crt" : "/etc/jabali/tls/panel.crt",
    attempt_count: 0,
    staging: false,
    use_le: false,
    updated_at: "2026-10-18T00:00:00Z",
    routable: true,
    ...over,
  };
}

function served(over: Record<string, unknown> = {}) {
  return {
    type: "self_signed",
    issuer: "Jabali Panel",
    subject: "panel.example.com",
    names: ["panel.example.com", "mail.panel.example.com", "localhost", "127.0.0.1", "203.0.113.10"],
    not_before: "2026-10-17T06:00:00Z",
    not_after: "2036-10-17T06:00:00Z",
    fingerprint_sha256: FP,
    ...over,
  };
}

function renderTab(certs: unknown[]) {
  get.mockResolvedValue({ status: 200, data: { certs } });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        <App>
          <PanelSSLTab />
        </App>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

// The inner card for one cert, found by its name in the card title.
async function certCard(name: string) {
  const title = await screen.findByText(name, { selector: ".ant-card-head code" });
  return title.closest(".ant-card") as HTMLElement;
}

beforeEach(() => {
  vi.clearAllMocks();
  setPanelClock("UTC", "DD/MM/YY HH:mm");
});
afterEach(() => cleanup());

describe("SSL Manager → Panel SSL details (GH #2166)", () => {
  it("shows what each cert file holds", async () => {
    const mailUntil = new Date(Date.now() + 60 * DAY).toISOString();
    renderTab([
      cert("hostname", { served: served() }),
      cert("mail", {
        status: "issued",
        use_le: false,
        expires_at: mailUntil,
        served: served({
          type: "lets_encrypt",
          issuer: "Let's Encrypt",
          subject: "mail.panel.example.com",
          names: ["mail.panel.example.com"],
          not_after: mailUntil,
        }),
      }),
    ]);

    const host = within(await certCard("panel.example.com"));
    expect(host.getByText("Self-signed", { selector: "td > span" })).toBeInTheDocument();
    expect(host.getByText("Jabali Panel")).toBeInTheDocument();
    for (const n of ["panel.example.com", "mail.panel.example.com", "localhost", "127.0.0.1", "203.0.113.10"]) {
      expect(host.getByText(n, { selector: ".ant-tag code" })).toBeInTheDocument();
    }
    expect(host.getByText("17/10/26")).toBeInTheDocument();
    expect(host.getByText("17/10/36")).toBeInTheDocument();
    expect(host.getByText(FP)).toBeInTheDocument();

    const mail = within(await certCard("mail.panel.example.com"));
    // Its type and its issuer.
    expect(mail.getAllByText("Let's Encrypt", { selector: "td > span" })).toHaveLength(2);
    expect(mail.getByText(/^Expires in (59|60) days$/)).toBeInTheDocument();
  });

  it("warns from 30 days before expiry, and once expired", async () => {
    const soon = new Date(Date.now() + 10.5 * DAY).toISOString();
    const gone = new Date(Date.now() - DAY).toISOString();
    renderTab([
      cert("hostname", { served: served({ not_after: soon }) }),
      cert("mail", { served: served({ subject: "mail.panel.example.com", not_after: gone }) }),
    ]);
    const soonTag = within(await certCard("panel.example.com")).getByText("Expires in 10 days");
    expect(soonTag.closest(".ant-tag")).toHaveClass("ant-tag-warning");
    const goneTag = within(await certCard("mail.panel.example.com")).getByText("Expired");
    expect(goneTag.closest(".ant-tag")).toHaveClass("ant-tag-error");
  });

  it("names a kept CA cert's own issuer, not Let's Encrypt", async () => {
    renderTab([
      cert("hostname", {
        status: "issued",
        served: served({ type: "ca", issuer: "Example Trust Services" }),
      }),
      cert("mail", { served: served({ subject: "mail.panel.example.com" }) }),
    ]);
    const host = within(await certCard("panel.example.com"));
    expect(host.getByText("Issued by Example Trust Services")).toBeInTheDocument();
    expect(host.getByText("Certificate authority")).toBeInTheDocument();
    expect(host.queryByText(/Issued by Let's Encrypt/)).not.toBeInTheDocument();
  });

  it("says why a cert has no usable file", async () => {
    renderTab([
      cert("hostname", { served: served() }),
      cert("mail", { served_error: "No certificate at /etc/jabali/tls/panel-mail.crt" }),
    ]);
    const mail = within(await certCard("mail.panel.example.com"));
    expect(mail.getByText("No certificate at /etc/jabali/tls/panel-mail.crt")).toBeInTheDocument();
    expect(mail.queryByText("Valid until")).not.toBeInTheDocument();
  });

  it("explains the mode it is in", async () => {
    renderTab([cert("hostname", { served: served() }), cert("mail")]);
    expect(await screen.findByText(/Let's Encrypt is off: the panel serves its own self-signed certificate/)).toBeInTheDocument();
    cleanup();
    renderTab([cert("hostname", { use_le: true, served: served() }), cert("mail", { use_le: true })]);
    expect(await screen.findByText(/Let's Encrypt issues and renews two independent certificates/)).toBeInTheDocument();
  });
});
