// DomainEmailSection.test.tsx — turning email off while mailboxes are left.
//
// Since mail-off domains route by MX (PR #2350), turning email off means mail
// for the domain stops arriving here: senders that reach this server are
// refused, and the server's own mail goes to the domain's MX. Mailboxes are
// kept, but new mail for them no longer arrives. The switch asks first when
// mailboxes are left, and still asks when the count can't be read.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DomainEmailSection } from "./DomainEmailSection";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

// mailboxTotal: a number is the list total; an Error makes the count request fail.
function mockGets(mailboxTotal: number | Error) {
  mocked.get.mockReset().mockImplementation((url: string) => {
    if (url === "/domains/d1/email") {
      return Promise.resolve({
        data: { domain_id: "d1", domain_name: "ex.test", email_enabled: true, records: [] },
      });
    }
    if (url.startsWith("/domains/d1/mailboxes")) {
      if (mailboxTotal instanceof Error) return Promise.reject(mailboxTotal);
      return Promise.resolve({ data: { data: [], total: mailboxTotal } });
    }
    return Promise.resolve({ data: { webmail_enabled: true } }); // /domains/d1
  });
}

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <DomainEmailSection domainId="d1" />
      </App>
    </QueryClientProvider>,
  );
}

// The email switch is the first one; webmail and DMARC switches follow it.
async function emailSwitch(): Promise<HTMLElement> {
  return (await screen.findAllByRole("switch"))[0];
}

async function flipOff() {
  const sw = await emailSwitch();
  await waitFor(() => expect(sw).toHaveAttribute("aria-checked", "true"));
  fireEvent.click(sw);
}

beforeEach(() => {
  mocked.delete.mockReset().mockResolvedValue({ data: {} });
});

describe("DomainEmailSection: email off with mailboxes left", () => {
  it("asks first when mailboxes are left; Cancel leaves email on", async () => {
    mockGets(2);
    renderSection();
    await flipOff();

    expect(await screen.findByText(/2 mailboxes on ex\.test are kept/)).toBeInTheDocument();
    expect(screen.getByText(/no longer arrives on this server/)).toBeInTheDocument();
    expect(mocked.delete).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    // happy-dom never finishes the close animation, so the modal stays in the
    // DOM; what matters is that email stays on. Let pending work settle first.
    await new Promise((r) => setTimeout(r, 50));
    expect(mocked.delete).not.toHaveBeenCalled();
    expect(await emailSwitch()).toHaveAttribute("aria-checked", "true");
  });

  it("turns email off after the confirm", async () => {
    mockGets(1);
    renderSection();
    await flipOff();

    expect(await screen.findByText(/1 mailbox on ex\.test is kept/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Turn off email" }));
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/domains/d1/email"));
  });

  it("no mailboxes left: turns email off without asking", async () => {
    mockGets(0);
    renderSection();
    await flipOff();

    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/domains/d1/email"));
    expect(screen.queryByRole("button", { name: "Turn off email" })).not.toBeInTheDocument();
  });

  it("mailbox count unreadable: still asks", async () => {
    mockGets(new Error("network"));
    renderSection();
    await flipOff();

    expect(await screen.findByText(/Mailboxes on ex\.test are kept/)).toBeInTheDocument();
    expect(mocked.delete).not.toHaveBeenCalled();
  });
});
