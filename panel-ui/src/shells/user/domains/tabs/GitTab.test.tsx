// GitTab.test — GH #2277. The tenant domain's Git tab: it explains why Git
// deployment is unavailable, connects a repository with the body the API
// expects, keeps or removes the saved token, puts the server's refusals on the
// right field, follows a deployment to its result (and reads the domain again,
// whose document root the first deployment moves), cancels one, offers a
// rollback only to a release a deployment completed, and shows a webhook
// secret the panel made once, in a dialog, never in the page after it.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());
const post = vi.hoisted(() => vi.fn());
const put = vi.hoisted(() => vi.fn());
const del = vi.hoisted(() => vi.fn());
vi.mock("../../../../apiClient", () => ({ apiClient: { get, post, put, delete: del } }));
const success = vi.hoisted(() => vi.fn());
const error = vi.hoisted(() => vi.fn());
vi.mock("../../../../lib/feedback", () => ({
  feedback: { message: { success, error, info: vi.fn() } },
}));

import { GitTab, type GitDeployment, type GitState } from "./GitTab";
import type { Domain } from "../../../../components/domains/types";

const domain = { id: "d1", name: "site.tld" } as Domain;
const base = "/domains/d1/git";
const REPO = "https://github.com/octocat/Hello-World.git";
const TOKEN = "git-token-abc_DEF-123"; // gitleaks:allow (test value)

const dep = (over: Partial<GitDeployment> = {}): GitDeployment => ({
  id: "01M5XN3T7020M5BAFVHG618WNN",
  kind: "deploy",
  source: "manual",
  release_id: "01M5XN3T7020M5BAFVHG618WNN",
  branch: "master",
  commit_sha: "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d",
  doc_root: "/home/u/domains/site.tld/current",
  php_version: "8.4",
  state: "succeeded",
  exit_code: 0,
  php_defense_blocked: false,
  log: "jabali-git-deploy: live 01M5XN3T7020M5BAFVHG618WNN",
  log_truncated: false,
  error: "",
  started_at: "2026-10-27T03:05:28Z",
  finished_at: "2026-10-27T03:05:44Z",
  ...over,
});

const saved = (over: Partial<GitState> = {}): GitState => ({
  enabled: true,
  configured: true,
  repo_url: REPO,
  branch: "master",
  username: "",
  has_token: false,
  verified_at: "2026-10-27T03:04:00Z",
  web_dir: "",
  shared_paths: [],
  deploy_commands: "",
  active_deployment: null,
  ...over,
});

const empty: GitState = {
  enabled: true,
  configured: false,
  has_token: false,
  web_dir: "",
  shared_paths: [],
  deploy_commands: "",
  active_deployment: null,
};

type Release = { id: string; live: boolean; deployment: GitDeployment | null };
type Hook = Record<string, unknown>;
const hookOff: Hook = { configured: false, queued: false, last_delivery_at: null, last_result: "" };

// What the mocked server answers; a test may change it as it goes.
const server: {
  state: GitState;
  runs: GitDeployment[];
  one: () => GitDeployment;
  releases: Release[];
  hook: Hook;
} = { state: empty, runs: [], one: () => dep(), releases: [], hook: hookOff };

function serve(over: Partial<typeof server>) {
  Object.assign(server, { state: empty, runs: [], one: () => dep(), releases: [], hook: hookOff }, over);
}

function apiError(status: number, body: Record<string, unknown>, message: string) {
  return Object.assign(new Error(message), { response: { status, data: body } });
}

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidate = vi.spyOn(qc, "invalidateQueries");
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <GitTab domain={domain} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { invalidate };
}

const popover = () => document.querySelector(".ant-popover:not(.ant-popover-hidden)") as HTMLElement;

beforeEach(() => {
  for (const fn of [get, post, put, del, success, error]) fn.mockReset();
  get.mockImplementation(async (url: string) => {
    if (url === base) return { data: server.state };
    if (url === `${base}/deployments`)
      return { data: { data: server.runs, total: server.runs.length, page: 1, page_size: 10 } };
    if (url.startsWith(`${base}/deployments/`)) return { data: server.one() };
    if (url === `${base}/releases`) return { data: { releases: server.releases, live: "" } };
    if (url === `${base}/webhook`) return { data: server.hook };
    if (url === `${base}/branches`) return { data: { default_branch: "master", branches: ["master", "test"] } };
    throw new Error(`unexpected GET ${url}`);
  });
});

describe("GitTab (GH #2277)", () => {
  it("says why Git deployment is unavailable and offers nothing", async () => {
    serve({ state: { ...empty, enabled: false, reason: "git_deploy_not_in_package" } });
    renderTab();
    expect(await screen.findByText(/doesn't include Git deployment/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Connect/ })).not.toBeInTheDocument();
  });

  it("connects a repository with the body the API expects", async () => {
    serve({});
    put.mockImplementation(async () => {
      server.state = saved();
      return { data: { ...saved(), remote: { default_branch: "master", branches: ["master"] } } };
    });
    renderTab();
    fireEvent.change(await screen.findByLabelText("Repository URL"), { target: { value: ` ${REPO} ` } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(base, {
        repo_url: REPO,
        branch: "",
        username: "",
        token: "",
        clear_token: false,
        web_dir: "",
        shared_paths: [],
        deploy_commands: "",
      }),
    );
    expect(await screen.findByRole("button", { name: /Deploy now/ })).toBeInTheDocument();
    expect(screen.getByText(REPO)).toBeInTheDocument();
    expect(success).toHaveBeenCalledWith("Connected. Deployments use the master branch.");
  });

  it("refuses an SSH URL or one with credentials before sending", async () => {
    serve({});
    renderTab();
    const url = await screen.findByLabelText("Repository URL");
    fireEvent.change(url, { target: { value: "git@github.com:octocat/Hello-World.git" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(await screen.findByText(/Enter the repository's https:\/\/ URL/)).toBeInTheDocument();
    fireEvent.change(url, { target: { value: "ssh://git@github.com/octocat/Hello-World.git" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(await screen.findByText(/SSH URLs \(git@…\) are not accepted/)).toBeInTheDocument();
    fireEvent.change(url, { target: { value: "https://me:pw@github.com/octocat/Hello-World.git" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(await screen.findByText(/Leave the username and token out of the URL/)).toBeInTheDocument();
    expect(put).not.toHaveBeenCalled();
  });

  it("keeps the saved token when the field is left empty, and removes it for a public repository", async () => {
    serve({ state: saved({ has_token: true, username: "octocat", shared_paths: [".env"], web_dir: "public" }) });
    put.mockResolvedValue({ data: saved({ has_token: true }) });
    renderTab();
    expect(await screen.findByText("Access token saved for octocat")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Edit settings/ }));
    const drawer = await screen.findByRole("dialog");
    fireEvent.click(within(drawer).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(put.mock.calls[0][1]).toEqual({
      repo_url: REPO,
      branch: "master",
      username: "octocat",
      token: "",
      clear_token: false,
      web_dir: "public",
      shared_paths: [".env"],
      deploy_commands: "",
    });

    fireEvent.click(await screen.findByRole("button", { name: /Edit settings/ }));
    const again = await screen.findByRole("dialog");
    fireEvent.change(within(again).getByLabelText("Access token"), { target: { value: TOKEN } });
    fireEvent.click(within(again).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    expect(put.mock.calls[1][1]).toMatchObject({ token: TOKEN, clear_token: false });

    // A token typed and then "public repository": the token is not sent.
    fireEvent.click(await screen.findByRole("button", { name: /Edit settings/ }));
    const third = await screen.findByRole("dialog");
    fireEvent.change(within(third).getByLabelText("Access token"), { target: { value: TOKEN } });
    fireEvent.click(within(third).getByRole("checkbox", { name: /Public repository/ }));
    fireEvent.click(within(third).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(put).toHaveBeenCalledTimes(3));
    expect(put.mock.calls[2][1]).toMatchObject({ token: "", clear_token: true });
  });

  it("asks for the token again when the repository moves to another host", async () => {
    serve({ state: saved({ has_token: true }) });
    put.mockRejectedValue(apiError(422, { error: "token_required_for_new_host", detail: "the stored token…" }, "x"));
    renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /Edit settings/ }));
    const drawer = await screen.findByRole("dialog");
    fireEvent.change(within(drawer).getByLabelText("Repository URL"), {
      target: { value: "https://gitlab.com/octocat/hello.git" },
    });
    fireEvent.click(within(drawer).getByRole("button", { name: "Save" }));
    expect(await within(drawer).findByText(/The saved token was given for another host/)).toBeInTheDocument();
  });

  it("puts a missing branch on the branch field and git's own words above the button", async () => {
    serve({});
    put.mockRejectedValueOnce(
      apiError(
        422,
        {
          error: "branch_not_found",
          detail: "the repository has no branch nope",
          remote: { default_branch: "master", branches: ["master"] },
        },
        "the repository has no branch nope",
      ),
    );
    put.mockRejectedValueOnce(
      apiError(422, { error: "git_remote_failed", detail: "fatal: Authentication failed" }, "fatal: Authentication failed"),
    );
    renderTab();
    fireEvent.change(await screen.findByLabelText("Repository URL"), { target: { value: REPO } });
    fireEvent.change(screen.getByLabelText("Branch"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    const branchItem = (await screen.findByText("the repository has no branch nope")).closest(".ant-form-item");
    expect(branchItem?.querySelector("label")?.textContent).toBe("Branch");

    fireEvent.change(screen.getByLabelText("Branch"), { target: { value: "master" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(
      await screen.findByText("The server couldn't read the repository: fatal: Authentication failed"),
    ).toBeInTheDocument();
  });

  it("deploys, follows the run to its end, and reads the domain again", async () => {
    let calls = 0;
    const running = dep({ state: "running", exit_code: null, commit_sha: "", log: "Cloning into…" });
    serve({
      state: saved(),
      one: () => (++calls === 1 ? running : dep({ log: "Cloning into…\njabali-git-deploy: live" })),
    });
    post.mockResolvedValue({ data: running });
    const { invalidate } = renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /Deploy now/ }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(`${base}/deployments`));
    const panel = await screen.findByRole("region", { name: "Git deployment" });
    expect(within(panel).getByText("Running")).toBeInTheDocument();
    expect(within(panel).getByText("Cloning into…")).toBeInTheDocument();
    expect(await within(panel).findByText("The new release is live.", undefined, { timeout: 4000 })).toBeInTheDocument();
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["one", "domains", "d1"] });
  });

  it("a deployment in progress locks Deploy and can be cancelled", async () => {
    const active = dep({ state: "running", exit_code: null, log: "Cloning into…" });
    serve({ state: saved({ active_deployment: active }), runs: [active], one: () => active });
    post.mockResolvedValue({ data: { ...active, state: "cancelled" } });
    renderTab();
    const panel = await screen.findByRole("region", { name: "Git deployment" });
    expect(screen.getByRole("button", { name: /Deploy now/ })).toBeDisabled();
    fireEvent.click(within(panel).getByRole("button", { name: /Cancel/ }));
    fireEvent.click(within(popover()).getByRole("button", { name: "Cancel deployment" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(`${base}/deployments/${active.id}/cancel`));
    expect(await screen.findByText(/The deployment was cancelled/)).toBeInTheDocument();
  });

  it("offers Make live only for an earlier release a deployment completed", async () => {
    const live = dep({ id: "01M5XNKPX7J3QCWCY2C6ZAWHHD" });
    const older = dep({ id: "01M5XNJWT9T7T6CTXDXAE536BW", commit_sha: "aaaaaaa1111" });
    const broken = dep({ id: "01M5XNJXMWJ06ZREQTKZKJ7ZVD", state: "failed", exit_code: 1 });
    serve({
      state: saved(),
      releases: [
        { id: live.id, live: true, deployment: live },
        { id: older.id, live: false, deployment: older },
        { id: broken.id, live: false, deployment: broken },
        { id: "01M5XN00000000000000000000", live: false, deployment: null },
      ],
    });
    post.mockResolvedValue({ data: dep({ kind: "rollback", state: "running", release_id: older.id }) });
    renderTab();
    await screen.findByText("master @ aaaaaaa");
    const buttons = screen.getAllByRole("button", { name: /Make live/ });
    expect(buttons).toHaveLength(1);
    fireEvent.click(buttons[0]);
    fireEvent.click(within(popover()).getByRole("button", { name: "Make live" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(`${base}/releases/${older.id}/activate`));
  });

  it("disconnects after asking", async () => {
    serve({ state: saved() });
    del.mockResolvedValue({});
    renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /Disconnect/ }));
    expect(within(popover()).getByText(/The site keeps serving its current release/)).toBeInTheDocument();
    fireEvent.click(within(popover()).getByRole("button", { name: "Disconnect" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith(base));
  });
});

describe("GitTab deploy on push (GH #2277)", () => {
  const URL = "https://panel.example:8443/api/v1/webhooks/git/abed552619dd7d18ccb26425fc928af5";
  const MADE = "webhook-secret-abc_DEF-123456"; // gitleaks:allow (test value)
  const HOSTS = "whsec_MDEyMzQ1Njc4OWFiY2RlZg=="; // gitleaks:allow (test value)

  it("shows the secret the panel made once, in a dialog, and never after", async () => {
    serve({ state: saved() });
    post.mockImplementation(async () => {
      server.hook = { configured: true, url: URL, queued: false, last_delivery_at: null, last_result: "" };
      return { data: { ...server.hook, secret: MADE } };
    });
    renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /Turn on/ }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(`${base}/webhook`, {}));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(MADE)).toBeInTheDocument();
    expect(within(dialog).getByText(URL)).toBeInTheDocument();
    expect(within(dialog).getByText(/not shown again/)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByText(MADE)).not.toBeInTheDocument());
    expect(screen.getByText(URL)).toBeInTheDocument();
    expect(screen.getByText("None yet")).toBeInTheDocument();
  });

  it("sends the Git host's own secret and shows no secret back", async () => {
    serve({ state: saved() });
    post.mockResolvedValue({
      data: { configured: true, url: URL, queued: false, last_delivery_at: null, last_result: "" },
    });
    renderTab();
    fireEvent.click(await screen.findByRole("button", { name: /Use my Git host's secret/ }));
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByLabelText("Secret");
    fireEvent.change(input, { target: { value: "too short" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    expect(await within(dialog).findByText("At least 16 characters")).toBeInTheDocument();
    fireEvent.change(input, { target: { value: HOSTS } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(`${base}/webhook`, { secret: HOSTS }));
    expect(success).toHaveBeenCalledWith("Webhook saved with your Git host's secret");
    expect(screen.queryByText(/not shown again/)).not.toBeInTheDocument();
  });

  it("says what the last delivery did, and that a push waits", async () => {
    serve({
      state: saved(),
      hook: {
        configured: true,
        url: URL,
        queued: true,
        last_delivery_at: "2026-10-27T03:14:09Z",
        last_result: "refused:git_deploy_not_in_package",
      },
    });
    renderTab();
    expect(
      await screen.findByText("Refused: the hosting package no longer includes Git deployment"),
    ).toBeInTheDocument();
    expect(screen.getByText(/A push waits for the running deployment/)).toBeInTheDocument();
  });

  it("names the saved branch for a delivery it ignored", async () => {
    serve({
      state: saved(),
      hook: { configured: true, url: URL, queued: false, last_delivery_at: "2026-10-27T03:14:09Z", last_result: "ignored" },
    });
    renderTab();
    expect(await screen.findByText("Ignored: not a push to master")).toBeInTheDocument();
  });
});
