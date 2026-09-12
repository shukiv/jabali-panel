// DomainSettingsButton.phase5.test.tsx — GH #1624 / ADR-0169 Phase 5.
//
// Phase 5 unifies the two config stores by making the typed Rule Builder the
// rendered source of truth while keeping raw directives *visible, not silently
// ignored*:
//   - Tenant view (TenantNginxRulesPanel): admin-authored raw directives
//     (nginx_custom_directives) are shown read-only under the builder, never
//     reverse-parsed into editable rules.
//   - Admin mirror (DomainNginxSection): the owner's tenant "advanced
//     directives" (nginx_tenant_directives, Phase 4a) are shown read-only with
//     a Clear action, because disabling the opt-in does not deactivate an
//     already-stored snippet.
//
// Only apiClient is mocked; the real AntD widgets and the in-file RuleBuilder
// render.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  TenantNginxRulesPanel,
  DomainNginxSection,
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

function renderAdmin(domain: DomainSettingsTarget) {
  const qc = new QueryClient();
  render(
    <QueryClientProvider client={qc}>
      <App>
        <DomainNginxSection domain={domain} />
      </App>
    </QueryClientProvider>,
  );
}

describe("ADR-0169 Phase 5 — tenant view surfaces admin raw directives", () => {
  beforeEach(() => {
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
  });

  it("surfaces admin raw directives read-only under the builder", () => {
    renderTenant({
      id: "d1",
      name: "x.com",
      nginx_custom_directives: "add_header X-Admin one;\nexpires 1h;",
      nginx_rules: [],
    });
    // Nothing that shapes the vhost is invisible to the owner.
    expect(screen.getByText("Administrator-managed directives")).toBeTruthy();
    // getByDisplayValue normalises whitespace (newlines -> spaces), so match a
    // single-line substring of the surfaced directives.
    const ta = screen.getByDisplayValue(
      /add_header X-Admin one;/,
    ) as HTMLTextAreaElement;
    // Surfaced, but NOT editable and NOT reverse-parsed into rules — read-only.
    expect(ta.readOnly).toBe(true);
  });

  it("hides the admin-directives block when the admin has set none", () => {
    renderTenant({
      id: "d1",
      name: "x.com",
      nginx_custom_directives: "",
      nginx_rules: [],
    });
    expect(screen.queryByText("Administrator-managed directives")).toBeNull();
  });
});

describe("ADR-0169 Phase 5 — admin mirror of tenant advanced directives", () => {
  beforeEach(() => {
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
  });

  it("shows the owner's directives read-only and clears them with an empty string", async () => {
    renderAdmin({
      id: "d1",
      name: "x.com",
      nginx_tenant_directives: "add_header X-Robots-Tag noindex;",
      nginx_rules: [],
    });
    expect(screen.getByText("Tenant advanced directives")).toBeTruthy();
    const ta = screen.getByDisplayValue(
      "add_header X-Robots-Tag noindex;",
    ) as HTMLTextAreaElement;
    expect(ta.readOnly).toBe(true);

    fireEvent.click(screen.getByText("Clear tenant directives"));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    const [url, body] = mocked.patch.mock.calls[0];
    expect(url).toBe("/domains/d1");
    // Must be "" (not null): a Go *string can't tell JSON null from an omitted
    // field, so null would read as "no change" and the snippet would persist.
    expect(body).toEqual({ nginx_tenant_directives: "" });
  });

  it("hides the tenant-directives mirror when the owner has set none", () => {
    renderAdmin({ id: "d1", name: "x.com", nginx_rules: [] });
    expect(screen.queryByText("Tenant advanced directives")).toBeNull();
  });
});
