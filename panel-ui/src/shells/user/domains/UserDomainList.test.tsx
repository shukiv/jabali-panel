// UserDomainList.test.tsx — GH #1543 + #1541. The tenant Domains list: the
// domain name links to its own Web Domain page, and a single "Add Web Domain"
// button opens the create drawer. DNS gating moved off the row menu (which is
// now just Enable/Delete) onto the Web Domain page's DNS tab — see
// WebDomainPage.test for the relocated GH #1419 coverage.
import { App } from "antd";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";

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

vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: { dns_enabled: true } }),
}));

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
