// ComposerTab.test — GH #2276. The tenant domain's Composer tab: it explains
// why Composer is unavailable, starts runs with the body the API expects
// (packages, --no-dev only where the command takes it), checks package names
// before sending, asks before Update and Remove, and follows a run to its
// result, including a cancel and a PHP Defense block.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());
const post = vi.hoisted(() => vi.fn());
vi.mock("../../../../apiClient", () => ({ apiClient: { get, post } }));
const error = vi.hoisted(() => vi.fn());
vi.mock("../../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error, info: vi.fn() } },
}));

import { ComposerTab, type ComposerRun } from "./ComposerTab";
import type { Domain } from "../../../../components/domains/types";

const domain = { id: "d1", name: "site.tld" } as Domain;
const base = "/domains/d1/composer";

const run = (over: Partial<ComposerRun> = {}): ComposerRun => ({
  id: "r1",
  app_dir: "",
  command: "install",
  packages: [],
  no_dev: false,
  php_version: "8.4",
  state: "succeeded",
  exit_code: 0,
  php_defense_blocked: false,
  log: "Installing dependencies from lock file",
  log_truncated: false,
  error: "",
  started_at: "2026-10-26T01:13:09Z",
  finished_at: "2026-10-26T01:13:22Z",
  ...over,
});

type State = Record<string, unknown>;
const enabled = (over: State = {}): State => ({
  enabled: true,
  detect: { dirs: [{ dir: "", has_lock: true, has_vendor: true }], php_version: "8.4" },
  active_run: null,
  ...over,
});

function serve(state: State, runs: ComposerRun[] = [], one?: ComposerRun) {
  get.mockImplementation(async (url: string) => {
    if (url === base) return { data: state };
    if (url === `${base}/runs`) return { data: { data: runs, total: runs.length, page: 1, page_size: 10 } };
    if (url.startsWith(`${base}/runs/`)) return { data: one ?? runs[0] };
    throw new Error(`unexpected GET ${url}`);
  });
}

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ComposerTab domain={domain} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  error.mockReset();
});

describe("ComposerTab (GH #2276)", () => {
  it("says why Composer is unavailable and offers no actions", async () => {
    serve({ enabled: false, reason: "composer_not_in_package", active_run: null });
    renderTab();
    expect(await screen.findByText(/doesn't include Composer runs/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Install/ })).not.toBeInTheDocument();
  });

  it("says where it looked when the domain has no composer.json", async () => {
    serve(enabled({ detect: { dirs: [], php_version: "8.4" } }));
    renderTab();
    expect(await screen.findByText(/No composer.json in this domain's document root/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Install/ })).not.toBeInTheDocument();
  });

  it("shows why the server can't run Composer for the domain", async () => {
    serve(enabled({ detect: { dirs: [], php_error: "site.tld does not run PHP: enable PHP on the domain first" } }));
    renderTab();
    expect(await screen.findByText(/does not run PHP/)).toBeInTheDocument();
  });

  it("Install sends the folder and no packages, then shows the run", async () => {
    const started = run({ state: "running", exit_code: null, log: "" });
    serve(enabled(), [], started);
    post.mockResolvedValue({ data: started });
    renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /Install/ }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(`${base}/runs`, {
        command: "install",
        app_dir: "",
        packages: [],
        no_dev: false,
      }),
    );
    const panel = await screen.findByRole("region", { name: "Composer run" });
    expect(within(panel).getByText("Running")).toBeInTheDocument();
    expect(within(panel).getByRole("button", { name: /Cancel/ })).toBeInTheDocument();
  });

  it("sends --no-dev only with a command that takes it", async () => {
    serve(enabled());
    post.mockResolvedValue({ data: run() });
    renderTab();
    fireEvent.click(await screen.findByRole("checkbox", { name: /Production only/ }));
    fireEvent.click(screen.getByRole("button", { name: /Install/ }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post.mock.calls[0][1]).toMatchObject({ command: "install", no_dev: true });

    fireEvent.click(screen.getByRole("button", { name: /More/ }));
    fireEvent.click(await screen.findByText("Validate composer.json"));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    expect(post.mock.calls[1][1]).toMatchObject({ command: "validate", no_dev: false });
  });

  it("Add package checks the name first, then sends name:constraint", async () => {
    serve(enabled());
    post.mockResolvedValue({ data: run({ command: "require" }) });
    renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /Add package/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Package"), { target: { value: "Not A Package" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add package" }));
    expect(await within(dialog).findByText(/looks like vendor\/name/)).toBeInTheDocument();
    expect(post).not.toHaveBeenCalled();

    fireEvent.change(within(dialog).getByLabelText("Package"), { target: { value: "monolog/monolog" } });
    fireEvent.change(within(dialog).getByLabelText(/Version/), { target: { value: "^3.0" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add package" }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(`${base}/runs`, {
        command: "require",
        app_dir: "",
        packages: ["monolog/monolog:^3.0"],
        no_dev: false,
      }),
    );
  });

  it("Update asks first, says to back up, and may update every package", async () => {
    serve(enabled());
    post.mockResolvedValue({ data: run({ command: "update" }) });
    renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /Update/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/Take a backup first/)).toBeInTheDocument();
    expect(post).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Run update" }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(`${base}/runs`, expect.objectContaining({ command: "update", packages: [] })),
    );
  });

  it("Remove needs at least one package", async () => {
    serve(enabled());
    renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /Remove package/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    expect(await within(dialog).findByText("Name at least one package")).toBeInTheDocument();
    expect(post).not.toHaveBeenCalled();
  });

  it("a run in progress locks the actions and can be cancelled", async () => {
    const active = run({ state: "running", exit_code: null, command: "update", log: "Loading composer repositories" });
    serve(enabled({ active_run: active }), [active], active);
    post.mockResolvedValue({ data: { ...active, state: "cancelled" } });
    renderTab();
    const panel = await screen.findByRole("region", { name: "Composer run" });
    expect(screen.getByRole("button", { name: /Install/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: /Update/ })).toBeDisabled();
    fireEvent.click(within(panel).getByRole("button", { name: /Cancel/ }));
    expect(await screen.findByText(/stay changed/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel run" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(`${base}/runs/r1/cancel`));
    expect(await screen.findByText("The run was cancelled.")).toBeInTheDocument();
  });

  it("shows a failed run's exit code and a PHP Defense block", async () => {
    const failed = run({ state: "failed", exit_code: 255, php_defense_blocked: true, log: "[snuffleupagus][drop]" });
    serve(enabled(), [failed], failed);
    renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /View log/ }));
    expect(await screen.findByText("The run failed (exit 255).")).toBeInTheDocument();
    expect(screen.getByText(/PHP Defense blocked a function/)).toBeInTheDocument();
  });

  it("tells the user when the server refuses to start a run", async () => {
    serve(enabled());
    post.mockRejectedValue(new Error("a Composer run is already in progress for this domain"));
    renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /Install/ }));
    await waitFor(() => expect(error).toHaveBeenCalledWith("a Composer run is already in progress for this domain"));
  });
});
