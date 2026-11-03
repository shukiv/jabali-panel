// DomainSecurityAddButtons.test.tsx — GH #2328 follow-up. On a domain's
// Security tab each list's Add button sits above its list, under the
// section's heading, so it is clear which list it adds to (it used to sit
// below the list, right above the next section's heading).
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";
import { DomainIPACLSection } from "./DomainIPACLSection";
import { DomainBotExclusionSection } from "./DomainBotExclusionSection";
import { DomainDirectoryPrivacySection } from "./DomainDirectoryPrivacySection";

const get = (apiClient as unknown as { get: ReturnType<typeof vi.fn> }).get;

function rowsFor(url: string): unknown[] {
  if (url.endsWith("/credentials")) {
    return [{ id: "c1", rule_id: "r1", username: "alice", created_at: "" }];
  }
  if (url.includes("/directory-privacy")) {
    return [{ id: "r1", domain_id: "d1", path: "/secret", realm: "Private", created_at: "", updated_at: "" }];
  }
  if (url.includes("/bot-challenge-exclusions")) {
    return [{ id: "e1", domain_id: "d1", path_prefix: "/api/hook", methods: [], note: "", created_at: "" }];
  }
  return [{ id: "a1", domain_id: "d1", cidr: "203.0.113.0/24", action: "allow", priority: 100, comment: "", created_at: "" }];
}

beforeEach(() => {
  vi.clearAllMocks();
  get.mockImplementation(async (url: string) => ({ data: { data: rowsFor(url), total: 1 } }));
});

function renderWith(node: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>{node}</App>
    </QueryClientProvider>,
  );
}

// True when the button comes before the table in the page.
function above(button: HTMLElement, table: HTMLElement | null): boolean {
  expect(table).not.toBeNull();
  return Boolean(button.compareDocumentPosition(table as HTMLElement) & Node.DOCUMENT_POSITION_FOLLOWING);
}

describe("Security tab: each Add button sits above its list (GH #2328)", () => {
  it("IP Allow / Deny", async () => {
    renderWith(<DomainIPACLSection domainId="d1" />);
    const table = (await screen.findByText("203.0.113.0/24")).closest("table");
    expect(above(screen.getByRole("button", { name: /Add rule/ }), table)).toBe(true);
  });

  it("Bot detection paths", async () => {
    renderWith(<DomainBotExclusionSection domainId="d1" domainName="shop.example" />);
    const table = (await screen.findByText("/api/hook")).closest("table");
    expect(above(screen.getByRole("button", { name: /Add path/ }), table)).toBe(true);
  });

  it("Directory privacy, and the users of a protected directory", async () => {
    renderWith(<DomainDirectoryPrivacySection domainId="d1" domainName="shop.example" />);
    const rules = (await screen.findByText("/secret")).closest("table");
    expect(above(screen.getByRole("button", { name: /Add protected directory/ }), rules)).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: /expand row/i }));
    const users = (await screen.findByText("alice")).closest("table");
    expect(above(await screen.findByRole("button", { name: /Add user/ }), users)).toBe(true);
  });
});
