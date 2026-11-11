// A domain whose mail is hosted elsewhere has only send-only accounts (#2491).
// Its drill-down says so and keeps the tabs that still apply: Accounts,
// Settings, Logs and Statistics. A link to another tab lands on Accounts.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MailDomainPage } from "./MailDomainPage";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

// The tabs fetch their own data; stand-ins show which one is rendered.
vi.mock("./tabs/MailboxesTab", () => ({ MailboxesTab: () => <div>accounts-tab</div> }));
vi.mock("./tabs/GroupsTab", () => ({ GroupsTab: () => <div>groups-tab</div> }));
vi.mock("./tabs/ForwardersTab", () => ({ ForwardersTab: () => <div>forwarders-tab</div> }));
vi.mock("./tabs/SettingsTab", () => ({ SettingsTab: () => <div>settings-tab</div> }));
vi.mock("./tabs/SharedFoldersTab", () => ({ SharedFoldersTab: () => <div>shared-tab</div> }));
vi.mock("./tabs/SharedResourcesTab", () => ({ SharedResourcesTab: () => <div>resources-tab</div> }));
vi.mock("./tabs/LogsTab", () => ({ LogsTab: () => <div>logs-tab</div> }));
vi.mock("./tabs/StatisticsTab", () => ({ StatisticsTab: () => <div>statistics-tab</div> }));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as { get: ReturnType<typeof vi.fn> };

function domain(overrides: Record<string, unknown>) {
  return { id: "d1", name: "sender.test", email_enabled: true, mail_provider: "jabali", ...overrides };
}

function Path() {
  return <div data-testid="path">{useLocation().pathname}</div>;
}

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/jabali-panel/mail-domains/:domainId" element={<MailDomainPage />} />
            <Route path="/jabali-panel/mail-domains/:domainId/:tab" element={<MailDomainPage />} />
          </Routes>
          <Path />
        </MemoryRouter>
      </App>
    </QueryClientProvider>,
  );
}

const tabLabels = () => screen.getAllByRole("tab").map((t) => t.textContent);

describe("MailDomainPage — mail hosted elsewhere", () => {
  beforeEach(() => mocked.get.mockReset());

  it("says the accounts can only send and shows Accounts, Settings, Logs and Statistics", async () => {
    mocked.get.mockResolvedValue({ data: domain({ email_enabled: false, mail_provider: "none" }) });
    renderAt("/jabali-panel/mail-domains/d1");

    expect(await screen.findByText("Mail for this domain is hosted elsewhere")).toBeInTheDocument();
    expect(tabLabels()).toEqual(["Accounts", "Settings", "Logs", "Statistics"]);
    expect(screen.getByText("accounts-tab")).toBeInTheDocument();
  });

  it("treats a domain with another provider the same way", async () => {
    mocked.get.mockResolvedValue({ data: domain({ mail_provider: "google" }) });
    renderAt("/jabali-panel/mail-domains/d1");

    expect(await screen.findByText("Mail for this domain is hosted elsewhere")).toBeInTheDocument();
    expect(tabLabels()).toEqual(["Accounts", "Settings", "Logs", "Statistics"]);
  });

  it("sends a link to another tab to Accounts", async () => {
    mocked.get.mockResolvedValue({ data: domain({ email_enabled: false, mail_provider: "none" }) });
    renderAt("/jabali-panel/mail-domains/d1/forwarders");

    expect(await screen.findByText("accounts-tab")).toBeInTheDocument();
    expect(screen.queryByText("forwarders-tab")).not.toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByTestId("path")).toHaveTextContent("/jabali-panel/mail-domains/d1/mailboxes"),
    );
  });

  it("keeps every tab for a domain whose mail is here", async () => {
    mocked.get.mockResolvedValue({ data: domain({}) });
    renderAt("/jabali-panel/mail-domains/d1/forwarders");

    expect(await screen.findByText("forwarders-tab")).toBeInTheDocument();
    expect(screen.queryByText("Mail for this domain is hosted elsewhere")).not.toBeInTheDocument();
    expect(tabLabels()).toContain("Forwarders");
    expect(screen.getByTestId("path")).toHaveTextContent("/jabali-panel/mail-domains/d1/forwarders");
  });
});
