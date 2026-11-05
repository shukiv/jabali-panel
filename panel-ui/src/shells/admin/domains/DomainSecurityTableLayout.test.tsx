// DomainSecurityTableLayout.test.tsx — GH #2328 follow-up. On a domain's
// Security tab, Save limits sits on its own row below the rate-limit inputs,
// and the free-text and path values in each list wrap at a set width, so a
// long value no longer widens its column and pushes the list past the card.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";
import { DomainIPACLSection } from "./DomainIPACLSection";
import { DomainRateLimitSection } from "./DomainRateLimitSection";
import { DomainBotExclusionSection } from "./DomainBotExclusionSection";
import { DomainDirectoryPrivacySection } from "./DomainDirectoryPrivacySection";

const get = (apiClient as unknown as { get: ReturnType<typeof vi.fn> }).get;

function rowsFor(url: string): unknown[] {
  if (url.includes("/directory-privacy")) {
    return [{ id: "r1", domain_id: "d1", path: "/secret", realm: "Private", created_at: "", updated_at: "" }];
  }
  if (url.includes("/bot-challenge-exclusions")) {
    return [{ id: "e1", domain_id: "d1", path_prefix: "/api/hook", methods: ["POST"], note: "payments", created_at: "" }];
  }
  return [{ id: "a1", domain_id: "d1", cidr: "203.0.113.0/24", action: "deny", priority: 10, comment: "scrapers", created_at: "" }];
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

// The wrapper that caps a value's width and lets it break anywhere.
const wraps = (el: HTMLElement) => el.closest(".jabali-wrap-cell") !== null;

describe("Security tab layout (GH #2328)", () => {
  it("Save limits sits on its own row, not beside the inputs", () => {
    renderWith(<DomainRateLimitSection domainId="d1" rateLimitRPS={0} connectionLimit={0} />);
    // A Form.Item is a block row. The bare button used to follow the
    // inline-flex row of inputs and land beside them.
    expect(screen.getByRole("button", { name: "Save limits" }).closest(".ant-form-item")).not.toBeNull();
  });

  it("IP Allow / Deny: the CIDR and comment wrap", async () => {
    renderWith(<DomainIPACLSection domainId="d1" />);
    expect(wraps(await screen.findByText("203.0.113.0/24"))).toBe(true);
    expect(wraps(screen.getByText("scrapers"))).toBe(true);
  });

  it("Bot detection paths: the path and note wrap", async () => {
    renderWith(<DomainBotExclusionSection domainId="d1" domainName="shop.example" />);
    expect(wraps(await screen.findByText("/api/hook"))).toBe(true);
    expect(wraps(screen.getByText("payments"))).toBe(true);
  });

  it("Directory privacy: the path and authentication name wrap", async () => {
    renderWith(<DomainDirectoryPrivacySection domainId="d1" domainName="shop.example" />);
    expect(wraps(await screen.findByText("/secret"))).toBe(true);
    expect(wraps(screen.getByText("Private"))).toBe(true);
  });
});
