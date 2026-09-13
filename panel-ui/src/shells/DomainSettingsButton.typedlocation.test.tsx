// DomainSettingsButton.typedlocation.test.tsx — GH #1624 typed location rules.
//
// The tenant Rule Builder must offer the two new panel-rendered kinds
// (Deny Paths, Static Cache) and must NOT offer the admin-only kinds
// (Proxy Pass, IP Access, PHP Setting) — the UI mirror of the backend
// tenantSafeNginxRuleTypes subset.
//
// Only apiClient is mocked; the real AntD widgets + in-file RuleBuilder render.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  TenantNginxRulesPanel,
  type DomainSettingsTarget,
} from "./DomainSettingsButton";

vi.mock("../apiClient", () => ({ apiClient: { patch: vi.fn() } }));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as { patch: ReturnType<typeof vi.fn> };

function renderTenant(domain: DomainSettingsTarget) {
  const qc = new QueryClient();
  render(
    <QueryClientProvider client={qc}>
      <App>
        <TenantNginxRulesPanel domain={domain} />
      </App>
    </QueryClientProvider>,
  );
}

const domain: DomainSettingsTarget = { id: "d1", name: "example.com", nginx_rules: [] };

describe("GH #1624 — tenant Rule Builder typed location rules", () => {
  beforeEach(() => {
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
  });

  it("offers Deny Paths and Static Cache but not the admin-only kinds", async () => {
    renderTenant(domain);
    // antd Dropdown opens on hover of the trigger button.
    fireEvent.mouseEnter(screen.getByRole("button", { name: /Add Rule/i }));
    await waitFor(() => expect(screen.getByText("Deny Paths")).toBeInTheDocument());
    expect(screen.getByText("Static Cache")).toBeInTheDocument();
    expect(screen.getByText("Custom Header")).toBeInTheDocument();
    expect(screen.getByText("Rewrite")).toBeInTheDocument();
    // Admin-only kinds must never appear in the tenant subset.
    expect(screen.queryByText("Proxy Pass")).not.toBeInTheDocument();
    expect(screen.queryByText("IP Access")).not.toBeInTheDocument();
    expect(screen.queryByText("PHP Setting")).not.toBeInTheDocument();
  });
});
