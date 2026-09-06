// DockerAppInventory.test.tsx — JAB-335. One common-action matrix invoked for
// BOTH real shells (AdminDockerAppsPage, UserDockerAppsPage), so the two Docker
// inventories cannot drift on the shared behavior, plus the load-bearing
// security assertions: the tenant UI must not render or dispatch
// exec / edit / update / backups, and its ports stay loopback-only regardless
// of the row's bind_interface.
//
// The admin privileged drawers are mocked to probes; the tenant's inline
// Logs/Credentials modals are real. Both api modules are mocked so no network
// happens. feedback.modal.confirm (the RowActions delete confirm) is
// short-circuited to its onOk so Delete reaches the delete fn.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ThemeModeProvider } from "../../theme/ThemeModeContext";
import type { InstalledApp } from "../../shells/admin/docker-apps/types";

const adminApi = vi.hoisted(() => ({
  listCatalog: vi.fn(),
  listInstalled: vi.fn(),
  lifecycleAction: vi.fn(),
  deleteApp: vi.fn(),
  updateApp: vi.fn(),
  // Privileged wire calls that the tenant must never reach.
  execCmd: vi.fn(),
  patchApp: vi.fn(),
  getEnv: vi.fn(),
  putEnv: vi.fn(),
  regenerateEnv: vi.fn(),
  fetchLogs: vi.fn(),
  listBackups: vi.fn(),
  createBackup: vi.fn(),
  restoreBackup: vi.fn(),
  installApp: vi.fn(),
}));
const userApi = vi.hoisted(() => ({
  listCatalog: vi.fn(),
  listInstalled: vi.fn(),
  lifecycleAction: vi.fn(),
  deleteApp: vi.fn(),
  fetchLogs: vi.fn(),
  fetchEnv: vi.fn(),
  fetchUsage: vi.fn(),
  installApp: vi.fn(),
  catalogIconUrl: vi.fn((slug: string) => `/api/v1/docker-apps/catalog/${slug}/icon`),
}));

vi.mock("../../shells/admin/docker-apps/api", () => adminApi);
vi.mock("../../shells/user/docker-apps/api", () => userApi);

// The tenant InstallModal mounts an ungated /domains query on the real
// apiClient; stub it so no test opens a socket.
vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn().mockResolvedValue({ data: { data: [] } }) },
}));

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (k: string) => k }) }));

// RowActions' delete confirm goes through feedback.modal.confirm — short-circuit
// to onOk so the delete action reaches the delete fn.
vi.mock("../../lib/feedback", () => ({
  feedback: {
    modal: { confirm: (o: { onOk?: () => void }) => o.onOk?.() },
    message: { success: vi.fn(), error: vi.fn() },
  },
}));

// Admin privileged drawers -> probes exposing open + the app id.
vi.mock("../../shells/admin/docker-apps/LogsDrawer", () => ({
  LogsDrawer: ({ open, appId }: { open: boolean; appId: string | null }) =>
    open ? <div data-testid="logs-drawer" data-appid={appId ?? ""} /> : null,
}));
vi.mock("../../shells/admin/docker-apps/ExecDrawer", () => ({
  ExecDrawer: ({ open, appId }: { open: boolean; appId: string | null }) =>
    open ? <div data-testid="exec-drawer" data-appid={appId ?? ""} /> : null,
}));
vi.mock("../../shells/admin/docker-apps/EditDrawer", () => ({
  EditDrawer: ({ open, app }: { open: boolean; app: InstalledApp | null }) =>
    open ? <div data-testid="edit-drawer" data-appid={app?.id ?? ""} /> : null,
}));
vi.mock("../../shells/admin/docker-apps/BackupsDrawer", () => ({
  BackupsDrawer: ({ open, appId }: { open: boolean; appId: string | null }) =>
    open ? <div data-testid="backups-drawer" data-appid={appId ?? ""} /> : null,
}));
vi.mock("../../shells/admin/docker-apps/InstallDrawer", () => ({
  InstallDrawer: () => null,
}));
vi.mock("../../shells/admin/docker-apps/EnvSection", () => ({
  EnvSection: ({ appId }: { appId: string }) => <div data-testid="env-section" data-appid={appId} />,
}));
vi.mock("../../shells/admin/docker-apps/MaintenanceTab", () => ({
  MaintenanceTab: () => <div data-testid="maintenance" />,
}));

import { AdminDockerAppsPage } from "../../shells/admin/docker-apps/AdminDockerAppsPage";
import { UserDockerAppsPage } from "../../shells/user/docker-apps/UserDockerAppsPage";

const makeApp = (over: Partial<InstalledApp>): InstalledApp => ({
  id: "j1",
  user_id: "u1",
  slug: "notes",
  name: "alpha",
  catalog_version: "1.0",
  image_sha: "sha-1",
  available_digest: null,
  last_check_at: null,
  backup_destination_id: null,
  status: "running",
  update_mode: "manual",
  cpu_limit: "1",
  memory_limit: "512m",
  pids_limit: 128,
  data_bytes: 0,
  size_checked_at: null,
  last_error: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  domain: "app.example.com",
  ports: [],
  ...over,
});

const renderAdapter = (Adapter: () => JSX.Element) => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidateSpy = vi.spyOn(qc, "invalidateQueries");
  const utils = render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ThemeModeProvider>
          <AntApp>
            <Adapter />
          </AntApp>
        </ThemeModeProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...utils, qc, invalidateSpy };
};

// Open the row's overflow ("More actions") menu and return its scoped queries.
const openRowMenu = async () => {
  fireEvent.click(screen.getByLabelText("More actions"));
  // menu items render into a portal; wait for a known shared item
  await screen.findByText("Restart");
};

interface MatrixOpts {
  Adapter: () => JSX.Element;
  api: { listInstalled: ReturnType<typeof vi.fn>; lifecycleAction: ReturnType<typeof vi.fn>; deleteApp: ReturnType<typeof vi.fn> };
  installedKey: unknown[];
  otherKey: unknown[];
  deleteLabel: string;
  privileged: boolean;
  loopbackOnly: boolean;
}

const commonMatrix = (label: string, opts: MatrixOpts) => {
  describe(`${label} docker inventory (common matrix)`, () => {
    beforeEach(() => {
      [...Object.values(adminApi), ...Object.values(userApi)].forEach((m) => m.mockReset());
      adminApi.listCatalog.mockResolvedValue([]);
      userApi.listCatalog.mockResolvedValue([]);
      userApi.fetchUsage.mockResolvedValue({ used_bytes: 0, quota_bytes: 0, over_quota: false });
      userApi.fetchLogs.mockResolvedValue({ slug: "notes", logs: "hello" });
      userApi.fetchEnv.mockResolvedValue([]);
      userApi.catalogIconUrl.mockImplementation((slug: string) => `/api/v1/docker-apps/catalog/${slug}/icon`);
      adminApi.updateApp.mockResolvedValue({ status: "updating", id: "j1" });
      opts.api.lifecycleAction.mockResolvedValue(undefined);
      opts.api.deleteApp.mockResolvedValue(undefined);
    });

    it("renders installed rows", async () => {
      opts.api.listInstalled.mockResolvedValue([makeApp({ id: "j1", name: "alpha" })]);
      renderAdapter(opts.Adapter);
      expect(await screen.findByText("alpha")).toBeInTheDocument();
    });

    it("filters by name / slug / domain", async () => {
      opts.api.listInstalled.mockResolvedValue([
        makeApp({ id: "j1", name: "alpha", slug: "notes", domain: "a.example.com" }),
        makeApp({ id: "j2", name: "beta", slug: "wiki", domain: "b.example.com" }),
      ]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.change(screen.getByRole("searchbox"), { target: { value: "wiki" } });
      expect(screen.queryByText("alpha")).not.toBeInTheDocument();
      expect(screen.getByText("beta")).toBeInTheDocument();
    });

    it("stops a running app through the shared lifecycle and invalidates only its own key", async () => {
      opts.api.listInstalled.mockResolvedValue([makeApp({ id: "j1", status: "running" })]);
      const { invalidateSpy } = renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.click(screen.getByText("Stop"));
      await waitFor(() => expect(opts.api.lifecycleAction).toHaveBeenCalledWith("j1", "stop"));
      await waitFor(() =>
        expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: opts.installedKey }),
      );
      expect(invalidateSpy).not.toHaveBeenCalledWith({ queryKey: opts.otherKey });
    });

    it("starts a stopped app through the shared lifecycle", async () => {
      opts.api.listInstalled.mockResolvedValue([makeApp({ id: "j1", status: "stopped" })]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.click(screen.getByText("Start"));
      await waitFor(() => expect(opts.api.lifecycleAction).toHaveBeenCalledWith("j1", "start"));
    });

    it("restarts through the overflow menu", async () => {
      opts.api.listInstalled.mockResolvedValue([makeApp({ id: "j1" })]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      await openRowMenu();
      fireEvent.click(screen.getByText("Restart"));
      await waitFor(() => expect(opts.api.lifecycleAction).toHaveBeenCalledWith("j1", "restart"));
    });

    it("deletes through the overflow menu (confirm short-circuited)", async () => {
      opts.api.listInstalled.mockResolvedValue([makeApp({ id: "j1" })]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      await openRowMenu();
      fireEvent.click(screen.getByText(opts.deleteLabel));
      await waitFor(() => expect(opts.api.deleteApp).toHaveBeenCalled());
      expect(opts.api.deleteApp.mock.calls[0][0]).toBe("j1");
    });

    it("surfaces the backend detail when a lifecycle action fails", async () => {
      opts.api.listInstalled.mockResolvedValue([makeApp({ id: "j1", status: "running" })]);
      opts.api.lifecycleAction.mockRejectedValue({ response: { data: { detail: "engine offline" } } });
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.click(screen.getByText("Stop"));
      expect(await screen.findByText("engine offline")).toBeInTheDocument();
    });

    it("renders ports with the audience's presentation regardless of bind_interface", async () => {
      opts.api.listInstalled.mockResolvedValue([
        makeApp({
          id: "j1",
          ports: [
            {
              id: "p1",
              app_id: "j1",
              port_name: "http",
              container_port: 80,
              bind_interface: "0.0.0.0", // public on the wire...
              host_port: 8080,
              protocol: "tcp",
              reverse_proxy: false,
              enabled: true,
              created_at: "2026-01-01T00:00:00Z",
            },
          ],
        }),
      ]);
      const { container } = renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      if (opts.loopbackOnly) {
        // ...but the tenant forces loopback regardless of bind_interface.
        expect(screen.getByText("loopback:8080/tcp")).toBeInTheDocument();
        expect(container.querySelector('a[href^="http://127.0.0.1:8080"]')).not.toBeNull();
        expect(screen.queryByText("public:8080/tcp")).not.toBeInTheDocument();
      } else {
        expect(screen.getByText("public:8080/tcp")).toBeInTheDocument();
        expect(container.querySelector('a[href^="http://localhost:8080"]')).not.toBeNull();
      }
    });

    it(`${opts.privileged ? "offers" : "hides"} the privileged verbs (exec / edit / update / backups)`, async () => {
      opts.api.listInstalled.mockResolvedValue([makeApp({ id: "j1", status: "stopped" })]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      await openRowMenu();
      for (const verb of ["Exec", "Edit", "Update", "Backups"]) {
        if (opts.privileged) {
          expect(screen.getByText(verb)).toBeInTheDocument();
        } else {
          expect(screen.queryByText(verb)).not.toBeInTheDocument();
        }
      }
    });
  });
};

commonMatrix("admin", {
  Adapter: AdminDockerAppsPage,
  api: adminApi,
  installedKey: ["docker-apps-installed"],
  otherKey: ["user-docker-installed"],
  deleteLabel: "Uninstall",
  privileged: true,
  loopbackOnly: false,
});

commonMatrix("tenant", {
  Adapter: UserDockerAppsPage,
  api: userApi,
  installedKey: ["user-docker-installed"],
  otherKey: ["docker-apps-installed"],
  deleteLabel: "Delete",
  privileged: false,
  loopbackOnly: true,
});

// ---- audience-specific: overlays + privileged dispatch --------------------

describe("admin docker inventory (privileged wiring)", () => {
  beforeEach(() => {
    [...Object.values(adminApi), ...Object.values(userApi)].forEach((m) => m.mockReset());
    adminApi.listCatalog.mockResolvedValue([]);
    adminApi.updateApp.mockResolvedValue({ status: "updating", id: "j1" });
    adminApi.lifecycleAction.mockResolvedValue(undefined);
  });

  it("dispatches Update through the admin api and invalidates the admin key", async () => {
    adminApi.listInstalled.mockResolvedValue([makeApp({ id: "j1", status: "stopped", available_digest: "sha-2", image_sha: "sha-1" })]);
    const { invalidateSpy } = renderAdapter(AdminDockerAppsPage);
    await screen.findByText("alpha");
    await openRowMenu();
    fireEvent.click(screen.getByText("Update"));
    await waitFor(() => expect(adminApi.updateApp).toHaveBeenCalledWith("j1"));
    // The admin shell's updateImage.onSuccess hardcodes the installed key — pin it.
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["docker-apps-installed"] }),
    );
  });

  it("opens the exec drawer for a row", async () => {
    adminApi.listInstalled.mockResolvedValue([makeApp({ id: "j1", status: "stopped" })]);
    renderAdapter(AdminDockerAppsPage);
    await screen.findByText("alpha");
    await openRowMenu();
    fireEvent.click(screen.getByText("Exec"));
    const drawer = await screen.findByTestId("exec-drawer");
    expect(drawer.getAttribute("data-appid")).toBe("j1");
  });

  it("opens the log drawer for a row", async () => {
    adminApi.listInstalled.mockResolvedValue([makeApp({ id: "j1" })]);
    renderAdapter(AdminDockerAppsPage);
    await screen.findByText("alpha");
    await openRowMenu();
    fireEvent.click(screen.getByText("Logs"));
    const drawer = await screen.findByTestId("logs-drawer");
    expect(drawer.getAttribute("data-appid")).toBe("j1");
  });
});

describe("tenant docker inventory (overlays)", () => {
  beforeEach(() => {
    [...Object.values(adminApi), ...Object.values(userApi)].forEach((m) => m.mockReset());
    userApi.listCatalog.mockResolvedValue([]);
    userApi.fetchUsage.mockResolvedValue({ used_bytes: 0, quota_bytes: 0, over_quota: false });
    userApi.fetchLogs.mockResolvedValue({ slug: "notes", logs: "hello" });
    userApi.fetchEnv.mockResolvedValue([]);
    userApi.catalogIconUrl.mockImplementation((slug: string) => `/api/v1/docker-apps/catalog/${slug}/icon`);
    userApi.lifecycleAction.mockResolvedValue(undefined);
  });

  it("opens the inline Logs modal for a row", async () => {
    userApi.listInstalled.mockResolvedValue([makeApp({ id: "j1", name: "alpha" })]);
    renderAdapter(UserDockerAppsPage);
    await screen.findByText("alpha");
    await openRowMenu();
    fireEvent.click(screen.getByText("Logs"));
    expect(await screen.findByText("Logs — alpha")).toBeInTheDocument();
  });

  it("shows the not-enabled notice on a 403 and renders no table", async () => {
    userApi.listInstalled.mockRejectedValue({ response: { status: 403 } });
    userApi.listCatalog.mockRejectedValue({ response: { status: 403 } });
    renderAdapter(UserDockerAppsPage);
    expect(
      await screen.findByText("userdockerappspage.docker_apps_are_not_enabled_on_this_server"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
  });
});
