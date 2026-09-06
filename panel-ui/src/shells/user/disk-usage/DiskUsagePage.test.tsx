// DiskUsagePage.test.tsx — GH #1417. When the mail module is disabled the panel
// must not surface Email-related pieces on the Disk Usage page: the "Email"
// stat card, the "Email Mailboxes" breakdown, and its "View all mailboxes" link
// (which would redirect to a page the tenant can't reach). Gated on the shared
// server-capabilities mail flag, the same signal the sidebar uses.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

let mailEnabled = true;
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: { mail_enabled: mailEnabled } }),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const diskUsage = {
  computed_at: "2026-09-04T00:00:00Z",
  total_bytes: 1000,
  quota_bytes: 0,
  files: { bytes: 100, items: [] },
  email: { bytes: 50, items: [{ name: "noreply@site.tld", bytes: 50 }] },
  databases: { bytes: 200, items: [] },
};

vi.mock("../../../apiClient", () => ({
  apiClient: {
    get: vi.fn(async (url: string) => {
      // The Files & Folders tree auto-fetches the browse endpoint on mount.
      if (url.startsWith("/me/disk-usage/files")) {
        return { data: { path: "~", total: 0, entries: [] } };
      }
      return { data: diskUsage };
    }),
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
});
