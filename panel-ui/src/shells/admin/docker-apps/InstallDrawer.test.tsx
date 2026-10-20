// InstallDrawer.test.tsx — an app that does not start without a domain
// (catalog requires_domain) cannot be installed with the domain left empty.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { CatalogEntry } from "./types";

const api = vi.hoisted(() => ({ installApp: vi.fn() }));
vi.mock("./api", () => api);
vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn().mockResolvedValue({ data: { data: [] } }) },
}));
vi.mock("../../../hooks/useServerStatus", () => ({ useServerStatus: () => ({ data: undefined }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (k: string) => k }) }));

import { InstallDrawer } from "./InstallDrawer";

const entry = (requiresDomain: boolean): CatalogEntry => ({
  slug: "ghost",
  name: "Ghost",
  version: "6",
  description: "blog",
  icon: "icon.svg",
  update_mode: "manual",
  resources: { cpu: "1.0", memory: "512m" },
  volumes: [],
  ports: [],
  requires_domain: requiresDomain,
});

const renderDrawer = (e: CatalogEntry) =>
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <AntApp>
        <InstallDrawer open entry={e} onClose={() => {}} />
      </AntApp>
    </QueryClientProvider>,
  );

describe("InstallDrawer domain", () => {
  beforeEach(() => {
    api.installApp.mockReset();
    api.installApp.mockResolvedValue({});
  });

  it("refuses to install an app that needs a domain without one", async () => {
    renderDrawer(entry(true));
    fireEvent.change(screen.getByPlaceholderText("e.g. vault-prod"), { target: { value: "blog" } });
    fireEvent.click(screen.getByRole("button", { name: "Install" }));
    expect(await screen.findByText("installdrawer.domain_required")).toBeTruthy();
    expect(api.installApp).not.toHaveBeenCalled();
  });

  it("installs an app that does not need a domain without one", async () => {
    renderDrawer(entry(false));
    fireEvent.change(screen.getByPlaceholderText("e.g. vault-prod"), { target: { value: "blog" } });
    fireEvent.click(screen.getByRole("button", { name: "Install" }));
    await waitFor(() => expect(api.installApp).toHaveBeenCalledTimes(1));
    expect(screen.queryByText("installdrawer.domain_required")).toBeNull();
  });
});
