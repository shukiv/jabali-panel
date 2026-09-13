// DomainSettingsButton.nginximport.test.tsx — GH #1624 nginx-snippet import.
//
// The tenant Rule Builder has an "Import from an nginx config" section that
// POSTs to /nginx-import/preview and MERGES the returned typed rules into the
// builder (the owner still reviews + Saves). This test drives the convert →
// preview → add flow with a mocked endpoint.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  TenantNginxRulesPanel,
  type DomainSettingsTarget,
} from "./DomainSettingsButton";

vi.mock("../apiClient", () => ({ apiClient: { patch: vi.fn(), post: vi.fn() } }));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as {
  patch: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

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

describe("GH #1624 — nginx snippet import merges into the Rule Builder", () => {
  beforeEach(() => {
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
    mocked.post.mockReset();
  });

  it("converts a snippet and adds the returned rule to the builder", async () => {
    mocked.post.mockResolvedValue({
      data: {
        rules: [{ type: "deny_paths", extensions: ["env"] }],
        warnings: [{ line: 2, source: "proxy_pass ...", reason: "not supported", security: true }],
        notes: [],
      },
    });
    renderTenant(domain);

    fireEvent.change(
      screen.getByPlaceholderText(/paste an nginx snippet here/i),
      { target: { value: "location ~* \\.(env)$ { deny all; }" } },
    );
    fireEvent.click(screen.getByRole("button", { name: /^Convert$/i }));

    await waitFor(() =>
      expect(screen.getByText(/1 rule\(s\) ready to import/i)).toBeInTheDocument(),
    );
    // The security warning must surface.
    expect(screen.getByText(/review manually \(security relevant\)/i)).toBeInTheDocument();

    // POST went to the nginx-import endpoint.
    expect(mocked.post).toHaveBeenCalledWith(
      "/domains/d1/nginx-import/preview",
      expect.objectContaining({ content: expect.stringContaining("deny all") }),
    );

    fireEvent.click(screen.getByRole("button", { name: /Add to Rule Builder/i }));
    // The merged rule now shows as a Deny Paths card in the builder.
    await waitFor(() => expect(screen.getByText("Deny Paths")).toBeInTheDocument());
    expect(screen.getByText(/deny env/i)).toBeInTheDocument();
  });
});
