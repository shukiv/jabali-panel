// UserDomainList.test.tsx — GH #1419. The Domains list Actions menu showed a
// "DNS" entry even when the DNS module is off, leading to a page that only
// redirects + 403s. It must be hidden, gated on the shared server-capabilities
// dns flag (the same signal the sidebar uses). Real AntD table + dropdown run.
import { App } from "antd";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { UserDomainList } from "./UserDomainList";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("../../../apiClient", () => ({
  apiClient: { patch: vi.fn(), get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));

vi.mock("../../../hooks/useQueries", () => ({
  useDeleteMutation: () => ({ mutateAsync: vi.fn() }),
  useCreateMutation: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

const domainRow = {
  id: "d1",
  name: "site.tld",
  is_enabled: true,
  doc_root: "/home/alice/site.tld",
  destination: "",
  source: "",
  type: "",
  temp_url: "",
  temp_url_enabled: false,
  bot_challenge_include: false,
  ssl_state: "active",
  bytes_30d: 0,
};

vi.mock("../../../hooks/useTableURL", () => ({
  useTableURL: () => ({
    items: [domainRow],
    total: 1,
    isLoading: false,
    params: { page: 1, pageSize: 20, q: "", sort: "name", order: "asc" },
    setParams: vi.fn(),
  }),
}));

let dnsEnabled = true;
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: { dns_enabled: dnsEnabled } }),
}));

const openRowMenu = async () => {
  const moreBtn = await screen.findByRole("button", { name: /more/i }, { timeout: 5000 });
  fireEvent.click(moreBtn);
};

beforeEach(() => {
  dnsEnabled = true;
});

describe("UserDomainList DNS action gating (GH #1419)", () => {
  it("shows the DNS action when the DNS module is enabled", async () => {
    render(
      <MemoryRouter>
        <App>
          <UserDomainList />
        </App>
      </MemoryRouter>,
    );
    await openRowMenu();
    expect(await screen.findByText("DNS")).toBeInTheDocument();
  });

  it("hides the DNS action when the DNS module is disabled", async () => {
    dnsEnabled = false;
    render(
      <MemoryRouter>
        <App>
          <UserDomainList />
        </App>
      </MemoryRouter>,
    );
    await openRowMenu();
    // Another always-present action confirms the menu opened.
    expect(await screen.findByText("Redirects")).toBeInTheDocument();
    expect(screen.queryByText("DNS")).not.toBeInTheDocument();
  });
});

// GH #1543: on the tenant list the domain name links to its own Web Domain
// page (not the live site); a separate launch icon still opens the live site.
describe("UserDomainList domain name link (GH #1543)", () => {
  it("links the domain name to its Web Domain page", async () => {
    render(
      <MemoryRouter>
        <App>
          <UserDomainList />
        </App>
      </MemoryRouter>,
    );
    const nameLink = await screen.findByRole("link", { name: "site.tld" });
    expect(nameLink).toHaveAttribute("href", "/jabali-panel/domains/d1");
  });
});

// GH #1541: the old "Add" split (Web / DNS / Mail) is replaced by a single
// "Add Web Domain" button that opens the drawer in web mode.
describe("UserDomainList Add Web Domain button (GH #1541)", () => {
  it("shows a single Add Web Domain button that opens the web drawer", async () => {
    render(
      <MemoryRouter>
        <App>
          <UserDomainList />
        </App>
      </MemoryRouter>,
    );
    const btn = await screen.findByRole("button", { name: /add web domain/i });
    fireEvent.click(btn);
    // The drawer opened in web mode — its domain-name input appears.
    expect(await screen.findByPlaceholderText("e.g., example.com")).toBeInTheDocument();
  });
});
