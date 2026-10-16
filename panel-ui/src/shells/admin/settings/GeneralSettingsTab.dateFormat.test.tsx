// GH #2164: the admin picks the panel's date format in Server Settings →
// Server Time, next to the timezone. DD/MM/YY HH:mm is the default.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));
// The tab also renders these cards; each loads its own data.
vi.mock("./SSOMaintenanceCard", () => ({ SSOMaintenanceCard: () => null }));
vi.mock("./PanelSSLCard", () => ({ PanelSSLCard: () => null }));
vi.mock("./CloudflareTokenCard", () => ({ CloudflareTokenCard: () => null }));
vi.mock("./ModulesCard", () => ({ ModulesCard: () => null }));

import { apiClient } from "../../../apiClient";
import { setPanelClock } from "../../../utils/datetime";
import { GeneralSettingsTab } from "./ServerSettingsPage";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
};

const settings = {
  id: 1,
  hostname: "panel.example.com",
  public_ipv4: "203.0.113.5",
  public_ipv6: "",
  admin_email: "ops@example.com",
  timezone: "UTC",
  date_format: "DD/MM/YY HH:mm",
  root_terminal_enabled: false,
};

afterEach(() => {
  cleanup();
  mocked.get.mockReset();
  mocked.patch.mockReset();
  setPanelClock("UTC", undefined);
});

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidate = vi.spyOn(qc, "invalidateQueries");
  render(
    <QueryClientProvider client={qc}>
      <App>
        <GeneralSettingsTab />
      </App>
    </QueryClientProvider>,
  );
  return { invalidate };
}

describe("GeneralSettingsTab date format (GH #2164)", () => {
  it("offers every preset with a live example and saves the pick", async () => {
    mocked.get.mockResolvedValue({ status: 200, data: settings });
    mocked.patch.mockImplementation((_url: string, body: Record<string, unknown>) =>
      Promise.resolve({ status: 200, data: { ...settings, ...body } }),
    );
    const { invalidate } = renderTab();

    const field = await screen.findByLabelText("Date format");
    // The stored format is selected.
    await waitFor(() => {
      const item = field.closest(".ant-form-item") as HTMLElement;
      expect(within(item).getByText(/\(DD\/MM\/YY HH:mm\)$/)).toBeInTheDocument();
    });

    fireEvent.mouseDown(field);
    const option = await screen.findByText(/\(YYYY-MM-DD HH:mm\)$/, { selector: ".ant-select-item-option-content" });
    // Each option shows the current time in that format.
    expect(option.textContent).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2} {2}\(YYYY-MM-DD HH:mm\)$/);
    fireEvent.click(option);

    fireEvent.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    const [url, body] = mocked.patch.mock.calls[0];
    expect(url).toBe("/admin/settings");
    expect(body).toMatchObject({ date_format: "YYYY-MM-DD HH:mm", timezone: "UTC" });
    // GET /me carries the panel clock: refetch it so the new format shows at once.
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ["whoami"] }));
  });

  it("sends the default when the server has no format yet", async () => {
    mocked.get.mockResolvedValue({ status: 200, data: { ...settings, date_format: "" } });
    mocked.patch.mockImplementation((_url: string, body: Record<string, unknown>) =>
      Promise.resolve({ status: 200, data: { ...settings, ...body } }),
    );
    renderTab();
    await screen.findByLabelText("Date format");
    await waitFor(() => expect(screen.getByDisplayValue("panel.example.com")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    expect(mocked.patch.mock.calls[0][1]).toMatchObject({ date_format: "DD/MM/YY HH:mm" });
  });
});
