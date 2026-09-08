// DiskUsagePage.test.tsx — GH #1417. When the mail module is disabled the panel
// must not surface Email-related pieces on the Disk Usage page: the "Email"
// stat card, the "Email Mailboxes" breakdown, and its "View all mailboxes" link
// (which would redirect to a page the tenant can't reach). Gated on the shared
// server-capabilities mail flag, the same signal the sidebar uses.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

let mailEnabled = true;
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: { mail_enabled: mailEnabled } }),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

// A FRESH snapshot (computed just now) so the mail-gating tests below don't
// trip the GH #1439 auto-measure-on-stale path — those tests are about which
// cards render, not about measuring.
const fresh = () => new Date().toISOString();
const diskUsage = {
  computed_at: fresh(),
  total_bytes: 1000,
  quota_bytes: 0,
  files: { bytes: 100, items: [] },
  email: { bytes: 50, items: [{ name: "noreply@site.tld", bytes: 50 }] },
  databases: { bytes: 200, items: [] },
};

// getState lets a test control what GET /me/disk-usage returns (e.g. a stale or
// never-computed snapshot); postRefresh records the auto/manual refresh call.
// Hoisted so the vi.mock factory (itself hoisted above module init) can close
// over them without a TDZ error.
const { getState, postRefresh } = vi.hoisted(() => ({
  getState: { resp: null as unknown },
  postRefresh: vi.fn(),
}));

vi.mock("../../../apiClient", () => ({
  apiClient: {
    get: vi.fn(async (url: string) => {
      // The Files & Folders tree auto-fetches the browse endpoint on mount.
      if (url.startsWith("/me/disk-usage/files")) {
        return { data: { path: "~", total: 0, entries: [] } };
      }
      return { data: getState.resp };
    }),
    post: postRefresh,
  },
}));

import { DiskUsagePage } from "./DiskUsagePage";

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <DiskUsagePage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("DiskUsagePage mail-module gating (GH #1417)", () => {
  beforeEach(() => {
    mailEnabled = true;
    getState.resp = diskUsage;
    postRefresh.mockReset();
    postRefresh.mockResolvedValue({ data: { ...diskUsage, computed_at: fresh() } });
  });

  it("shows the Email Mailboxes section + View all mailboxes link when mail is enabled", async () => {
    renderPage();
    expect(await screen.findByText("Email Mailboxes")).toBeInTheDocument();
    expect(screen.getByText("View all mailboxes")).toBeInTheDocument();
  });

  it("hides the Email section + link when the mail module is disabled", async () => {
    mailEnabled = false;
    renderPage();
    // "Storage Quota Usage" always renders once loaded — unique, unlike
    // "Databases" (stat card + section both use it) — so wait on it.
    expect(await screen.findByText("Storage Quota Usage")).toBeInTheDocument();
    expect(screen.queryByText("Email Mailboxes")).not.toBeInTheDocument();
    expect(screen.queryByText("View all mailboxes")).not.toBeInTheDocument();
  });

  // A fresh snapshot must NOT auto-measure — otherwise every page open would
  // recompute and defeat the whole point of the cached snapshot.
  it("does not auto-refresh when the snapshot is fresh (GH #1439)", async () => {
    renderPage();
    expect(await screen.findByText("Storage Quota Usage")).toBeInTheDocument();
    expect(postRefresh).not.toHaveBeenCalled();
  });
});

describe("DiskUsagePage auto-measure on open (GH #1439, lxsdevcode)", () => {
  beforeEach(() => {
    mailEnabled = true;
    postRefresh.mockReset();
    postRefresh.mockResolvedValue({ data: { ...diskUsage, computed_at: fresh() } });
  });

  it("auto-measures once when there is no snapshot yet", async () => {
    getState.resp = {
      computed_at: null,
      total_bytes: 0,
      quota_bytes: 0,
      files: { bytes: 0, items: [] },
      email: { bytes: 0, items: [] },
      databases: { bytes: 0, items: [] },
    };
    renderPage();
    // The measure fires automatically…
    await waitFor(() => expect(postRefresh).toHaveBeenCalledTimes(1));
    // …and its result (a fresh snapshot) renders the real page.
    expect(await screen.findByText("Storage Quota Usage")).toBeInTheDocument();
  });

  it("auto-measures once when the snapshot is stale (older than a day)", async () => {
    getState.resp = {
      ...diskUsage,
      computed_at: new Date(Date.now() - 3 * 24 * 60 * 60 * 1000).toISOString(),
    };
    renderPage();
    await waitFor(() => expect(postRefresh).toHaveBeenCalledTimes(1));
  });

  // Regression guard for lxsdevcode's exact report: the nightly refresh-all
  // keeps snapshots just under a day old ("Computed 22 h ago"), and the earlier
  // 24h gate treated that as fresh, so the page never auto-measured on open. A
  // snapshot a couple of hours old must now trigger a measure (threshold is 1h).
  it("auto-measures when the snapshot is a few hours old (GH #1439 nightly-kept case)", async () => {
    getState.resp = {
      ...diskUsage,
      computed_at: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
    };
    renderPage();
    await waitFor(() => expect(postRefresh).toHaveBeenCalledTimes(1));
  });
});
