// GH #1387 (johnnyq, 2026-09-01): the Mail Domains list shows ONLY mail-active
// domains — the earlier "list every owned domain + a Status column + an Enable
// action" is reverted. What remains: SSL badge + a per-row Disable action
// (DELETE /domains/:id/email). Creating a mailbox happens inside a domain's
// drill-down, so there is no New Mailbox button on this list.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MailDomainsPage } from "./MailDomainsPage";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

// GH #1479: the Create button gates on the global mail capability. Mock it so
// the state is explicit (default: mail on).
const caps = vi.hoisted(() => ({ value: { mail_enabled: true } as Record<string, boolean> }));
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: caps.value }),
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

// The API only ever returns mail-active domains now, so every row here is
// mail-on. susp.test is a mail-on domain that is also bandwidth-suspended — it
// still lists (mail is active), the Status column that used to badge it is gone.
const ROWS = [
  {
    id: "d-on",
    name: "on.test",
    mailbox_count: 3,
    mail_bytes: 1024,
    sent_30d: 5,
    received_30d: 7,
    queue: 0,
    email_enabled: true,
    ssl_state: "active_le",
    is_quota_suspended: false,
  },
  {
    id: "d-susp",
    name: "susp.test",
    mailbox_count: 1,
    mail_bytes: 10,
    sent_30d: 0,
    received_30d: 0,
    email_enabled: true,
    ssl_state: "self_signed",
    is_quota_suspended: true,
  },
];

function mockList() {
  mocked.get
    .mockReset()
    .mockResolvedValue({ data: { data: ROWS, total: ROWS.length, page: 1, page_size: ROWS.length } });
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <MemoryRouter>
          <MailDomainsPage />
        </MemoryRouter>
      </App>
    </QueryClientProvider>,
  );
}

// GH #1387: actions live behind a per-row "⋯" dropdown. Open it and click the
// named menu item.
async function openRowMenu(onRow: HTMLElement, item: string) {
  fireEvent.click(within(onRow).getByRole("button", { name: /Actions for/i }));
  fireEvent.click(await screen.findByRole("menuitem", { name: item }));
}

beforeEach(() => {
  mockList();
  mocked.post.mockReset().mockResolvedValue({ data: {} });
  mocked.delete.mockReset().mockResolvedValue({ data: {} });
  caps.value = { mail_enabled: true };
});

describe("GH #1387 — MailDomainsPage (mail-active only)", () => {
  it("lists mail-active domains with SSL badges, no Status column, no Enable/New Mailbox", async () => {
    renderPage();
    await waitFor(() => expect(mocked.get).toHaveBeenCalledWith("/me/mail-domains"));
    await screen.findByText("on.test");
    await screen.findByText("susp.test");
    // SSL badge for the LE domain still renders.
    expect(screen.getByText("Let's Encrypt")).toBeInTheDocument();
    // Status column is gone: no Enabled/Disabled/Suspended badges.
    expect(screen.queryByText("Enabled")).not.toBeInTheDocument();
    expect(screen.queryByText("Disabled")).not.toBeInTheDocument();
    expect(screen.queryByText("Suspended")).not.toBeInTheDocument();
    // No Enable action (all listed domains are already active) and no
    // list-level New Mailbox button (creation lives in the drill-down).
    expect(screen.queryByRole("button", { name: "Enable" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /New Mailbox/i })).not.toBeInTheDocument();
  });

  it("actions collapse into a ⋯ menu (no inline Disable/Delete buttons)", async () => {
    renderPage();
    const onRow = (await screen.findByText("on.test")).closest("tr") as HTMLElement;
    // No visible action buttons in the row until the menu is opened.
    expect(within(onRow).queryByRole("button", { name: "Disable" })).not.toBeInTheDocument();
    expect(within(onRow).queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
    within(onRow).getByRole("button", { name: /Actions for/i }); // the ⋯ trigger exists
  });

  it("disables mail via the ⋯ menu → confirm → DELETE /domains/:id/email", async () => {
    renderPage();
    const onRow = (await screen.findByText("on.test")).closest("tr") as HTMLElement;
    await openRowMenu(onRow, "Disable");
    // A confirm modal pops (okText "Disable"); confirm it.
    fireEvent.click(await screen.findByRole("button", { name: "Disable" }));
    await waitFor(() =>
      expect(mocked.delete).toHaveBeenCalledWith("/domains/d-on/email"),
    );
    expect(mocked.post).not.toHaveBeenCalled();
  });

  it("deletes mail via the ⋯ menu → type-to-confirm → POST /domains/:id/email/purge", async () => {
    renderPage();
    const onRow = (await screen.findByText("on.test")).closest("tr") as HTMLElement;
    await openRowMenu(onRow, "Delete");

    // Modal opens; the destructive confirm is disabled until the name matches.
    const okBtn = await screen.findByRole("button", { name: /Delete mail/i });
    expect(okBtn).toBeDisabled();

    const input = screen.getByPlaceholderText("on.test");
    fireEvent.change(input, { target: { value: "wrong" } });
    expect(screen.getByRole("button", { name: /Delete mail/i })).toBeDisabled();

    fireEvent.change(input, { target: { value: "on.test" } });
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Delete mail/i })).toBeEnabled(),
    );
    fireEvent.click(screen.getByRole("button", { name: /Delete mail/i }));

    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/domains/d-on/email/purge", {
        confirm_domain: "on.test",
      }),
    );
    expect(mocked.delete).not.toHaveBeenCalled();
  });

  // GH #1479 (johnnyq): a Create Mail Domain button opens the Add-domain drawer
  // in mail mode, exposing the requested knobs — TLS (incl. None), Enable
  // webmail, Create DNS mail records.
  it("Create Mail Domain button opens the mail drawer with TLS/webmail/DNS fields", async () => {
    renderPage();
    await screen.findByText("on.test");

    fireEvent.click(screen.getByRole("button", { name: /Create Mail Domain/i }));

    // The shared Add-domain drawer opens in mail mode.
    expect(await screen.findByText("Add Mail Domain")).toBeInTheDocument();
    // #1479 knobs are present.
    expect(screen.getByRole("checkbox", { name: /Enable webmail/i })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /Create DNS mail records/i })).toBeChecked();
    // Domain-name field is there to type into.
    expect(screen.getByPlaceholderText(/example\.com/i)).toBeInTheDocument();
    // GH #1479 (johnnyq follow-up): the mail-provider select is web-only —
    // it must NOT appear in the Create Mail Domain drawer (mail is implied).
    expect(screen.queryByText("No mail")).not.toBeInTheDocument();
    expect(screen.queryByText("Microsoft 365")).not.toBeInTheDocument();
    expect(screen.queryByText("Jabali mail (this server)")).not.toBeInTheDocument();
  });

  // GH #1479 (johnnyq follow-up): when mail is disabled server-wide, offering to
  // create a mail domain makes no sense — the button is gone from both the
  // header and the empty state, and the empty state says mail is off.
  it("hides Create Mail Domain when mail is disabled server-wide", async () => {
    caps.value = { mail_enabled: false };
    renderPage();
    await screen.findByText("on.test"); // rows still load
    expect(screen.queryByRole("button", { name: /Create Mail Domain/i })).not.toBeInTheDocument();
  });

  it("shows a mail-off empty state (no Create button) when disabled and no domains", async () => {
    caps.value = { mail_enabled: false };
    mocked.get
      .mockReset()
      .mockResolvedValue({ data: { data: [], total: 0, page: 1, page_size: 0 } });
    renderPage();
    expect(await screen.findByText("Mail is disabled on this server.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Create Mail Domain/i })).not.toBeInTheDocument();
  });

  // GH #1612 (johnnyq): a mail domain whose DNS is hosted externally
  // (dns_disabled) gets an "External DNS" tag and a "DNS records" ⋯ action; a
  // panel-hosted domain gets neither (the panel publishes the records itself).
  const ROWS_DNS = [
    { ...ROWS[0], id: "d-int", name: "int.test", dns_disabled: false },
    { ...ROWS[0], id: "d-ext", name: "ext.test", dns_disabled: true },
  ];

  function mockDnsList() {
    mocked.get
      .mockReset()
      .mockResolvedValue({ data: { data: ROWS_DNS, total: 2, page: 1, page_size: 2 } });
  }

  it("GH #1612: external-DNS row is tagged and offers a DNS records action", async () => {
    mockDnsList();
    renderPage();
    const extRow = (await screen.findByText("ext.test")).closest("tr") as HTMLElement;
    const intRow = (await screen.findByText("int.test")).closest("tr") as HTMLElement;

    // Tag only on the external-DNS row.
    expect(within(extRow).getByText("External DNS")).toBeInTheDocument();
    expect(within(intRow).queryByText("External DNS")).not.toBeInTheDocument();

    // ⋯ menu offers "DNS records" on the external row.
    fireEvent.click(within(extRow).getByRole("button", { name: /Actions for/i }));
    expect(await screen.findByRole("menuitem", { name: "DNS records" })).toBeInTheDocument();
  });

  it("GH #1612: panel-hosted-DNS row has no DNS records action", async () => {
    mockDnsList();
    renderPage();
    const intRow = (await screen.findByText("int.test")).closest("tr") as HTMLElement;
    fireEvent.click(within(intRow).getByRole("button", { name: /Actions for/i }));
    // Menu opened (Disable present) but no DNS-records item.
    await screen.findByRole("menuitem", { name: "Disable" });
    expect(screen.queryByRole("menuitem", { name: "DNS records" })).not.toBeInTheDocument();
  });

  // DKIM/SPF are stored unquoted (raw provider form); the zone-file "Copy all"
  // must quote TXT rdata or a ";" truncates DMARC/DKIM/TLS-RPT on bulk import.
  const EMAIL_STATE = {
    domain_id: "d-ext",
    domain_name: "ext.test",
    email_enabled: true,
    records: [
      { purpose: "A — the mail host itself", name: "mail.ext.test.", type: "A", value: "203.0.113.9" },
      { purpose: "MX — delivers incoming mail", name: "ext.test.", type: "MX", value: "10 mail.ext.test." },
      { purpose: "SPF — authorises this host", name: "ext.test.", type: "TXT", value: "v=spf1 mx ~all" },
      { purpose: "DKIM — signs outbound mail", name: "jabali._domainkey.ext.test.", type: "TXT", value: "v=DKIM1;k=ed25519;p=AAAA" },
    ],
    warnings: [],
  };

  function mockDnsListAndEmail() {
    mocked.get.mockReset().mockImplementation((url: string) => {
      if (url === "/me/mail-domains") {
        return Promise.resolve({ data: { data: ROWS_DNS, total: 2, page: 1, page_size: 2 } });
      }
      if (url === "/domains/d-ext/email") return Promise.resolve({ data: EMAIL_STATE });
      return Promise.resolve({ data: {} });
    });
  }

  it("GH #1612: DNS records action opens a modal listing records with copy buttons", async () => {
    mockDnsListAndEmail();
    renderPage();

    const extRow = (await screen.findByText("ext.test")).closest("tr") as HTMLElement;
    await openRowMenu(extRow, "DNS records");

    // Modal opens, titled for the domain, listing the records + a copy-all.
    expect(await screen.findByText("DNS records for ext.test")).toBeInTheDocument();
    // Records load async after the email GET resolves.
    expect(await screen.findByText("203.0.113.9")).toBeInTheDocument();
    expect(screen.getByText("10 mail.ext.test.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Copy all/i })).toBeInTheDocument();
    // A per-value copy button exists (aria-labelled with the value).
    expect(screen.getByRole("button", { name: "Copy 203.0.113.9" })).toBeInTheDocument();
  });

  it("GH #1612: Copy all quotes TXT rdata so DKIM/SPF survive a zone import", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    mockDnsListAndEmail();
    renderPage();

    const extRow = (await screen.findByText("ext.test")).closest("tr") as HTMLElement;
    await openRowMenu(extRow, "DNS records");
    await screen.findByText("203.0.113.9");

    fireEvent.click(screen.getByRole("button", { name: /Copy all/i }));
    await waitFor(() => expect(writeText).toHaveBeenCalled());
    const copied = writeText.mock.calls[0][0] as string;
    // TXT values are quoted; non-TXT (MX) is not.
    expect(copied).toContain('\tTXT\t"v=DKIM1;k=ed25519;p=AAAA"');
    expect(copied).toContain('\tTXT\t"v=spf1 mx ~all"');
    expect(copied).toContain("\tMX\t10 mail.ext.test.");
  });

  // GH #1479: the mail-mode create posts the right domain shape (web off, mail on
  // Jabali, ssl le, DNS on), and webmail-OFF fires a follow-up PATCH.
  it("submits a mail domain: POST /domains (web off, jabali mail) + webmail-off PATCH", async () => {
    mocked.post.mockReset().mockResolvedValue({ data: { id: "d-new" } });
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
    renderPage();
    await screen.findByText("on.test");

    fireEvent.click(screen.getByRole("button", { name: /Create Mail Domain/i }));
    await screen.findByText("Add Mail Domain");

    fireEvent.change(screen.getByPlaceholderText(/example\.com/i), {
      target: { value: "new.test" },
    });
    // Turn webmail OFF so the follow-up PATCH fires.
    fireEvent.click(screen.getByRole("checkbox", { name: /Enable webmail/i }));
    fireEvent.click(screen.getByRole("button", { name: /^Add$/ }));

    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith(
        "/domains",
        expect.objectContaining({
          name: "new.test",
          web_enabled: false,
          mail_provider: "jabali",
          ssl_mode: "le",
          manage_dns: true,
        }),
      ),
    );
    // enable_webmail is a drawer-only field — it must NOT be in the create body.
    expect(mocked.post.mock.calls[0][1]).not.toHaveProperty("enable_webmail");
    await waitFor(() =>
      expect(mocked.patch).toHaveBeenCalledWith("/domains/d-new", {
        webmail_enabled: false,
      }),
    );
  });
});
