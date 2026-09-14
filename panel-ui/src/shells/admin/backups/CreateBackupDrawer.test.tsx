// CreateBackupDrawer.test.tsx — regression for GH #182.
//
// The manual "Create backup" drawer showed an EMPTY user dropdown while
// the scheduled-backup form (SchedulesTab) showed users fine. Root
// cause: the drawer queried `admin/users`, but the user LIST route is
// `GET /api/v1/users` (`/admin/users` only has `:id/...` action
// sub-routes) — so the list 404'd and the Select rendered no options.
//
// These tests lock the wire contract: the drawer MUST hit `/users`
// (never `/admin/users`) and MUST render the non-admin accounts the
// list returns. Only apiClient is mocked; TanStack + AntD run for real.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { CreateBackupDrawer } from "./CreateBackupDrawer";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

// The drawer issues two list GETs on open: users + backup-destinations.
// Route on the URL substring; envelope is the standard {data,total}.
function mockLists(dests: Array<{ id: string; name: string; kind: string; enabled: boolean }> = []) {
  mocked.get.mockImplementation(async (url: string) => {
    if (url.startsWith("/users")) {
      return {
        data: {
          data: [
            { id: "u1", username: "alice", email: "alice@example.com", is_admin: false },
            { id: "u2", username: "root", email: "root@example.com", is_admin: true },
          ],
          total: 2,
        },
      };
    }
    if (url.includes("/backup-destinations")) {
      return { data: { data: dests, total: dests.length } };
    }
    throw new Error(`unexpected GET ${url}`);
  });
}

function renderDrawer() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <CreateBackupDrawer open onClose={() => {}} onCreated={() => {}} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mocked.get.mockReset();
  mocked.post.mockReset();
});

describe("CreateBackupDrawer — user list wire contract (GH #182)", () => {
  it("queries the /users list route, never /admin/users", async () => {
    mockLists();
    renderDrawer();

    await waitFor(() => {
      const urls = mocked.get.mock.calls.map((c) => String(c[0]));
      expect(urls.some((u) => /^\/users\?/.test(u))).toBe(true);
      expect(urls.some((u) => u.includes("/admin/users"))).toBe(false);
    });
  });

  it("renders non-admin accounts as selectable options and excludes admins", async () => {
    mockLists();
    renderDrawer();

    // Open the User select; AntD renders options in a body portal.
    const userSelect = await screen.findByText("Pick a user");
    fireEvent.mouseDown(userSelect);

    // alice (non-admin) is selectable; root (is_admin) is filtered out.
    expect(await screen.findByText(/alice \(alice@example\.com\)/)).toBeInTheDocument();
    expect(screen.queryByText(/root \(root@example\.com\)/)).toBeNull();
  });
});

describe("CreateBackupDrawer — System compression (GH #1646 Slice 1)", () => {
  it("shows the compression control for a System backup (not account-only)", async () => {
    mockLists();
    renderDrawer();

    // Switch to System — the compression control must render for it, not just
    // for an account backup (it lived inside the account-only block before).
    fireEvent.click(await screen.findByText("System"));
    expect(await screen.findByText("Compression")).toBeInTheDocument();
  });

  it("carries the compression level in the system backup POST body", async () => {
    mockLists([{ id: "d1", name: "Local", kind: "local", enabled: true }]);
    renderDrawer();

    fireEvent.click(await screen.findByText("System"));

    // Pick the (only) destination — a system create requires one.
    fireEvent.mouseDown(await screen.findByText("Pick a destination"));
    fireEvent.click(await screen.findByText("Local (local)"));

    fireEvent.click(screen.getByRole("button", { name: "Create backup" }));

    await waitFor(() => expect(mocked.post).toHaveBeenCalled());
    const [url, body] = mocked.post.mock.calls[0] as [string, Record<string, unknown>];
    expect(url).toBe("/admin/system/backups");
    // The key must be present (default "" = restic auto) so the operator's
    // choice reaches the agent; before GH #1646 the system POST omitted it.
    expect(body).toHaveProperty("compression");
    expect(body).toMatchObject({ include_accounts: false });
  });
});

describe("CreateBackupDrawer — Full Server compression (GH #1646 Slice 2)", () => {
  it("shows the compression control for a Full Server backup", async () => {
    mockLists();
    renderDrawer();

    // Slice 2 extends the control to Full Server so the level propagates to the
    // fanned-out per-account jobs, not just the system leg.
    fireEvent.click(await screen.findByText("Full Server"));
    expect(await screen.findByText("Compression")).toBeInTheDocument();
  });

  it("carries the compression level in the Full Server POST body", async () => {
    mockLists([{ id: "d1", name: "Local", kind: "local", enabled: true }]);
    renderDrawer();

    fireEvent.click(await screen.findByText("Full Server"));

    fireEvent.mouseDown(await screen.findByText("Pick a destination"));
    fireEvent.click(await screen.findByText("Local (local)"));

    fireEvent.click(screen.getByRole("button", { name: "Create backup" }));

    await waitFor(() => expect(mocked.post).toHaveBeenCalled());
    const [url, body] = mocked.post.mock.calls[0] as [string, Record<string, unknown>];
    expect(url).toBe("/admin/system/backups");
    // include_accounts:true is what makes this a Full Server run; the
    // compression key must ride along so the fan-out stamps it on each job.
    expect(body).toHaveProperty("compression");
    expect(body).toMatchObject({ include_accounts: true });
  });
});
