// An account on a domain whose mail is hosted elsewhere is send-only (#2491).
// Webmail opens at mail.<domain>, which such a domain doesn't have, and
// automatic replies answer mail it never receives, so neither is offered.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MailboxesTab } from "./MailboxesTab";

vi.mock("../../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../../apiClient";

const mocked = apiClient as unknown as { get: ReturnType<typeof vi.fn> };

const t = "2026-11-11T10:00:00Z";
const list = (data: unknown[]) => ({ data: { data, total: data.length, page: 1, page_size: 20 } });
const domain = (id: string, name: string, email_enabled: boolean, mail_provider: string) => ({
  id, user_id: "u1", name, email_enabled, mail_provider, is_enabled: true, doc_root: "", created_at: t, updated_at: t,
});
const mailbox = (id: string, domain_id: string, email: string, send_only: boolean) => ({
  id, domain_id, email, display_name: "", quota_bytes: 1 << 30, is_disabled: false, send_only,
  last_usage_bytes: 0, created_at: t, updated_at: t,
});

beforeEach(() => {
  mocked.get.mockReset().mockImplementation((url: string) => {
    if (url.startsWith("/domains?") || url === "/domains")
      return Promise.resolve(list([
        domain("d-here", "example.com", true, "jabali"),
        domain("d-send", "notify.test", false, "none"),
      ]));
    if (url.startsWith("/me/mailboxes"))
      return Promise.resolve(list([
        mailbox("mb-here", "d-here", "alice@example.com", false),
        mailbox("mb-send", "d-send", "orders@notify.test", true),
      ]));
    return Promise.resolve(list([]));
  });
});

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <MemoryRouter>
          <MailboxesTab />
        </MemoryRouter>
      </App>
    </QueryClientProvider>,
  );
}

const rowOf = async (email: string) =>
  (await screen.findByText(email)).closest("tr") as HTMLElement;

describe("MailboxesTab — accounts on a domain whose mail is hosted elsewhere", () => {
  it("offers no Webmail or Automatic replies for them", async () => {
    renderTab();
    const sendRow = await rowOf("orders@notify.test");
    // Wait for the domains list, which decides the actions.
    await within(sendRow).findByRole("button", { name: /Edit/ });
    expect(within(sendRow).queryByRole("button", { name: /Webmail/ })).toBeNull();

    fireEvent.click(within(sendRow).getByRole("button", { name: "More actions" }));
    await screen.findByRole("menuitem", { name: /Rotate password/ });
    expect(screen.queryByRole("menuitem", { name: /Automatic replies/ })).toBeNull();
  });

  it("keeps both for an account on a domain whose mail is here", async () => {
    renderTab();
    const hereRow = await rowOf("alice@example.com");
    expect(await within(hereRow).findByRole("button", { name: /Webmail/ })).toBeInTheDocument();

    fireEvent.click(within(hereRow).getByRole("button", { name: "More actions" }));
    expect(await screen.findByRole("menuitem", { name: /Automatic replies/ })).toBeInTheDocument();
  });
});
