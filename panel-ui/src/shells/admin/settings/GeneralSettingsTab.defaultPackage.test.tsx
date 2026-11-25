// Server Settings → General → Migrations: the package a migration gives an
// account it creates without a package picked. "" clears it.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));
vi.mock("./SSOMaintenanceCard", () => ({ SSOMaintenanceCard: () => null }));
vi.mock("./PanelSSLCard", () => ({ PanelSSLCard: () => null }));
vi.mock("./CloudflareTokenCard", () => ({ CloudflareTokenCard: () => null }));
vi.mock("./ModulesCard", () => ({ ModulesCard: () => null }));

import { apiClient } from "../../../apiClient";
import { GeneralSettingsTab } from "./ServerSettingsPage";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
};

const TIER1 = "01TIER1PKG0000000000000000";
const TIER3 = "01TIER3PKG0000000000000000";
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

function serve(defaultPackageID: string | null) {
  mocked.get.mockImplementation((url: string) =>
    Promise.resolve({
      status: 200,
      data: url.startsWith("/packages")
        ? { data: [{ id: TIER1, name: "Tier 1" }, { id: TIER3, name: "Tier 3" }], total: 2 }
        : { ...settings, default_package_id: defaultPackageID },
    }),
  );
  mocked.patch.mockImplementation((_url: string, body: Record<string, unknown>) =>
    Promise.resolve({ status: 200, data: { ...settings, ...body } }),
  );
}

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <GeneralSettingsTab />
      </App>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  mocked.get.mockReset();
  mocked.patch.mockReset();
});

describe("GeneralSettingsTab default package for migrations", () => {
  it("shows the stored package and saves a new pick", async () => {
    serve(TIER3);
    renderTab();
    const field = await screen.findByLabelText("Default package for migrations");
    const item = field.closest(".ant-form-item") as HTMLElement;
    await waitFor(() => expect(within(item).getByText("Tier 3")).toBeInTheDocument());

    fireEvent.mouseDown(field);
    fireEvent.click(await screen.findByText("Tier 1", { selector: ".ant-select-item-option-content" }));
    fireEvent.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    expect(mocked.patch.mock.calls[0][1]).toMatchObject({ default_package_id: TIER1 });
  });

  it("sends an empty id when there is no default package", async () => {
    serve(null);
    renderTab();
    await screen.findByLabelText("Default package for migrations");
    await waitFor(() => expect(screen.getByDisplayValue("panel.example.com")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    expect(mocked.patch.mock.calls[0][1]).toMatchObject({ default_package_id: "" });
  });
});
