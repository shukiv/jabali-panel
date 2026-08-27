// GH #1234 — the Mail Stats storage drilldown was mailbox-anchored, so a domain
// with no mailboxes (mail disabled) never appeared. It is now domain-anchored and
// tags the mail-off ones. This pins that a mail-disabled domain shows up under
// its owner with a "Mail off" tag.
//
// Only apiClient is mocked; the real AntD Table + expandable rows run.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({ apiClient: { get: vi.fn() } }));
import { apiClient } from "../../../apiClient";
import { MailStatsTab } from "./MailStatsTab";

const mocked = apiClient as unknown as { get: ReturnType<typeof vi.fn> };

const payload = {
  points: {},
  rates: {},
  current: {},
  storage: [
    {
      username: "alice",
      domain_name: "shop.example",
      mail_enabled: true,
      email: "a@shop.example",
      usage_bytes: 1024,
      quota_bytes: 4096,
    },
    // Mail-disabled domain: arrives as one row with an empty email + 0 usage.
    {
      username: "alice",
      domain_name: "off.example",
      mail_enabled: false,
      email: "",
      usage_bytes: 0,
      quota_bytes: 0,
    },
  ],
  traffic: [],
  by_user: [],
};

afterEach(cleanup);

function renderTab() {
  mocked.get.mockResolvedValue({ data: payload });
  const qc = new QueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <App>
        <MailStatsTab />
      </App>
    </QueryClientProvider>,
  );
}

describe("GH #1234 — mail-off domains show in the storage drilldown", () => {
  it("lists a mail-disabled domain under its owner, tagged 'Mail off'", async () => {
    const { container } = renderTab();

    // Owner row renders once the stats load.
    await screen.findByText("alice");

    // Expand the owner row to reveal its domains (storage is the only table
    // with rows, so the first expand icon is alice's).
    const expandIcon = container.querySelector(
      ".ant-table-row-expand-icon",
    ) as HTMLElement;
    expect(expandIcon).toBeTruthy();
    fireEvent.click(expandIcon);

    // Both the mail-consuming domain AND the mail-off domain appear…
    await screen.findByText("shop.example");
    expect(screen.getByText("off.example")).toBeInTheDocument();
    // …and the disabled one is tagged.
    expect(screen.getByText("Mail off")).toBeInTheDocument();
  });
});
