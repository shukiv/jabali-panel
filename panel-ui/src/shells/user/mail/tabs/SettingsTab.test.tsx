// SettingsTab — GH #1628 slice 4: tenant per-domain webmail toggle.
//
// The core assertion is the owner-scoped PATCH /domains/:id { webmail_enabled }
// on flip (RED if the mutation is dropped), plus the email-off gate that mirrors
// the admin domain-email section (switch hidden until the domain has mail on).
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { SettingsTab } from "./SettingsTab";

vi.mock("../../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
};

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <App>
        <SettingsTab domainId="d1" />
      </App>
    </QueryClientProvider>,
  );
}

describe("GH #1628 slice 4 — SettingsTab per-domain webmail toggle", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("toggles the per-domain webmail flag via PATCH /domains/:id", async () => {
    mocked.get.mockResolvedValue({
      data: { id: "d1", name: "on.test", email_enabled: true, webmail_enabled: true },
    });
    mocked.patch.mockResolvedValue({ data: {} });

    renderTab();

    const sw = await screen.findByRole("switch", { name: "Webmail client" });
    expect(sw).toBeChecked();

    fireEvent.click(sw);

    await waitFor(() =>
      expect(mocked.patch).toHaveBeenCalledWith("/domains/d1", { webmail_enabled: false }),
    );
  });

  it("hides the toggle and prompts to enable email when the domain has mail off", async () => {
    mocked.get.mockResolvedValue({
      data: { id: "d1", name: "off.test", email_enabled: false, webmail_enabled: true },
    });

    renderTab();

    expect(
      await screen.findByText(/enable email for this domain first/i),
    ).toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Webmail client" })).toBeNull();
    expect(mocked.patch).not.toHaveBeenCalled();
  });
});
