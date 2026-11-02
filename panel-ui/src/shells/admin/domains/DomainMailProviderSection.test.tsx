// DomainMailProviderSection.test.tsx — moving mail away from this server while
// mailboxes are left.
//
// Since PR #2350 a domain whose provider is not Jabali mail is not local any
// more: mail for it no longer arrives here, mailboxes are kept but get no new
// mail. Saving a provider other than Jabali asks first when the domain had
// Jabali mail with email on and mailboxes left.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DomainMailProviderSection } from "./DomainMailProviderSection";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
};

function mockGets(mailboxTotal: number, emailEnabled = true) {
  mocked.get.mockReset().mockImplementation((url: string) => {
    if (url === "/domains/d1/email") {
      return Promise.resolve({
        data: { domain_id: "d1", domain_name: "ex.test", email_enabled: emailEnabled, records: [] },
      });
    }
    if (url.startsWith("/domains/d1/mailboxes")) {
      return Promise.resolve({ data: { data: [], total: mailboxTotal } });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderSection(mailProvider: string): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <DomainMailProviderSection domainId="d1" mailProvider={mailProvider} />
      </App>
    </QueryClientProvider>,
  );
  return qc;
}

function pick(label: string) {
  fireEvent.mouseDown(screen.getByRole("combobox"));
  fireEvent.click(screen.getByText(label));
}

function save() {
  fireEvent.click(screen.getByRole("button", { name: /Save mail provider/ }));
}

beforeEach(() => {
  mocked.patch.mockReset().mockResolvedValue({ data: {} });
});

describe("DomainMailProviderSection: mail moved away with mailboxes left", () => {
  it("Jabali → Google with mailboxes left asks first; PATCH only after OK", async () => {
    mockGets(3);
    renderSection("jabali");
    pick("Google Workspace");
    save();

    expect(await screen.findByText(/3 mailboxes on this domain are kept/)).toBeInTheDocument();
    expect(screen.getByText(/no longer arrives on this server/)).toBeInTheDocument();
    expect(mocked.patch).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Change provider" }));
    await waitFor(() =>
      expect(mocked.patch).toHaveBeenCalledWith("/domains/d1", {
        mail_provider: "google",
        m365_onmicrosoft: "",
        google_dkim: "",
      }),
    );
  });

  it("Cancel saves nothing", async () => {
    mockGets(3);
    renderSection("jabali");
    pick("No mail");
    save();

    await screen.findByText(/3 mailboxes on this domain are kept/);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    // happy-dom never finishes the close animation, so the modal stays in the
    // DOM; what matters is that nothing is saved. Let pending work settle first.
    await new Promise((r) => setTimeout(r, 50));
    expect(mocked.patch).not.toHaveBeenCalled();
  });

  it("no mailboxes left: saves without asking", async () => {
    mockGets(0);
    renderSection("jabali");
    pick("Microsoft 365");
    save();

    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "Change provider" })).not.toBeInTheDocument();
  });

  it("already elsewhere (Google → Microsoft 365): saves without asking", async () => {
    mockGets(3);
    renderSection("google");
    pick("Microsoft 365");
    save();

    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "Change provider" })).not.toBeInTheDocument();
  });

  it("email already off: saves without asking", async () => {
    mockGets(3, false);
    const qc = renderSection("jabali");
    // Wait for the email state to load, so the check below is not a race.
    await waitFor(() => expect(qc.getQueryData(["one", "domain-email", "d1"])).toBeDefined());
    pick("Google Workspace");
    save();

    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "Change provider" })).not.toBeInTheDocument();
  });
});
