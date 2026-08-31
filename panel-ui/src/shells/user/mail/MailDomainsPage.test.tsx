// GH #1387 follow-up (johnnyq): the Mail Domains list gained a Status column,
// an SSL column, and a per-row Enable/Disable mail action. The list now shows
// ALL owned domains (so a mail-off domain can be enabled in place) — the toggle
// reuses POST/DELETE /domains/:id/email.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MailDomainsPage } from "./MailDomainsPage";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

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
    id: "d-off",
    name: "off.test",
    mailbox_count: 0,
    mail_bytes: 0,
    sent_30d: 0,
    received_30d: 0,
    email_enabled: false,
    ssl_state: "off",
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

// The row trigger and the Popconfirm OK share the same label. Open the confirm
// from the given (row-scoped) trigger, then click the OK — it's portaled to
// document.body, so it's the LAST button carrying that label in DOM order.
async function openAndConfirm(trigger: HTMLElement, label: string) {
  const before = screen.getAllByRole("button", { name: label }).length;
  fireEvent.click(trigger);
  await waitFor(() =>
    expect(screen.getAllByRole("button", { name: label }).length).toBe(before + 1),
  );
  const all = screen.getAllByRole("button", { name: label });
  fireEvent.click(all[all.length - 1]);
}

beforeEach(() => {
  mockList();
  mocked.post.mockReset().mockResolvedValue({ data: {} });
  mocked.delete.mockReset().mockResolvedValue({ data: {} });
});

describe("GH #1387 — MailDomainsPage status + actions", () => {
  it("lists all owned domains with Status and SSL badges", async () => {
    renderPage();
    await waitFor(() => expect(mocked.get).toHaveBeenCalledWith("/me/mail-domains"));
    // All three rows render (mail-off domain is listed too).
    await screen.findByText("on.test");
    await screen.findByText("off.test");
    await screen.findByText("susp.test");
    // Status badges: Enabled / Disabled / Suspended.
    expect(screen.getByText("Disabled")).toBeInTheDocument();
    expect(screen.getByText("Suspended")).toBeInTheDocument();
    expect(screen.getAllByText("Enabled").length).toBeGreaterThanOrEqual(1);
    // SSL badge for the LE domain.
    expect(screen.getByText("Let's Encrypt")).toBeInTheDocument();
  });

  it("enables mail on a mail-off domain via POST /domains/:id/email", async () => {
    renderPage();
    const offRow = (await screen.findByText("off.test")).closest("tr") as HTMLElement;
    const enableBtn = within(offRow).getByRole("button", { name: "Enable" });
    await openAndConfirm(enableBtn, "Enable");
    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/domains/d-off/email"),
    );
    expect(mocked.delete).not.toHaveBeenCalled();
  });

  it("disables mail on a mail-on domain via DELETE /domains/:id/email", async () => {
    renderPage();
    const onRow = (await screen.findByText("on.test")).closest("tr") as HTMLElement;
    const disableBtn = within(onRow).getByRole("button", { name: "Disable" });
    await openAndConfirm(disableBtn, "Disable");
    await waitFor(() =>
      expect(mocked.delete).toHaveBeenCalledWith("/domains/d-on/email"),
    );
    expect(mocked.post).not.toHaveBeenCalled();
  });
});
