// UserDashboard.test.tsx
//
// GH #1417 — with the mail module disabled the dashboard must not surface Email
// pieces: the "Recent mailboxes" card and the "Mailboxes" stat tile both link to
// /mail/mailboxes, which redirects to an inaccessible page. Same
// server-capabilities gate the sidebar + Disk Usage use.
//
// JAB-370 Recent projection — the recent-mailboxes list now loads through ONE
// owner-scoped GET /me/mailboxes request instead of a per-domain fan-out over
// /domains/:id/mailboxes. The fan-out capped domains at 200 (silently truncating
// the list + total past that) and merged/sorted in the browser. The pin below
// proves exactly one /me/mailboxes call and zero per-domain calls, independent of
// how many domains the tenant owns.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

// Domains/apps/databases recent cards all read through useListQuery. Return a
// few email-enabled domains so a regression back to the per-domain fan-out would
// actually issue /domains/:id/mailboxes calls for the pin to catch.
const domainRows = [
  { id: "d1", name: "one.test", email_enabled: true, created_at: "2026-01-03" },
  { id: "d2", name: "two.test", email_enabled: true, created_at: "2026-01-02" },
  { id: "d3", name: "three.test", email_enabled: true, created_at: "2026-01-01" },
];
vi.mock("../../hooks/useQueries", () => ({
  useListQuery: () => ({ items: domainRows, total: domainRows.length, isLoading: false }),
}));

// Inspectable apiClient.get spy. /me/mailboxes returns two rows + an authoritative
// total (7) that exceeds the page; every other path returns an empty envelope.
const mockGet = vi.fn(async (url: string) => {
  if (url.startsWith("/me/mailboxes")) {
    return {
      data: {
        data: [
          { id: "m1", email: "newest@one.test", domain_name: "one.test", created_at: "2026-01-09" },
          { id: "m2", email: "older@two.test", domain_name: "two.test", created_at: "2026-01-08" },
        ],
        total: 7,
      },
    };
  }
  return { data: { data: [], total: 0 } };
});
vi.mock("../../apiClient", () => ({
  apiClient: { get: (url: string) => mockGet(url) },
}));

vi.mock("../../identity", () => ({ getIdentity: () => Promise.resolve(null) }));
vi.mock("./MyProfileUsageCard", () => ({ MyProfileUsageCard: () => null }));

let mailEnabled = true;
vi.mock("../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: { mail_enabled: mailEnabled } }),
}));

import { UserDashboard } from "./UserDashboard";

function renderDash() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <UserDashboard />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("UserDashboard mail-module gating (GH #1417)", () => {
  beforeEach(() => {
    mailEnabled = true;
    mockGet.mockClear();
  });

  it("shows the mailboxes card + stat tile when mail is enabled", async () => {
    renderDash();
    await waitFor(() => expect(screen.getByText("userdashboard.recent_mailboxes")).toBeInTheDocument());
    expect(screen.getByText("userdashboard.mailboxes")).toBeInTheDocument();
  });

  it("hides the mailboxes card + stat tile when the mail module is disabled", async () => {
    mailEnabled = false;
    renderDash();
    // Domains always renders — anchor on it, then assert the mail pieces are gone.
    await waitFor(() => expect(screen.getByText("userdashboard.recent_domains")).toBeInTheDocument());
    expect(screen.queryByText("userdashboard.recent_mailboxes")).not.toBeInTheDocument();
    expect(screen.queryByText("userdashboard.mailboxes")).not.toBeInTheDocument();
  });

  it("skips the /me/mailboxes fetch entirely when the mail module is disabled", async () => {
    mailEnabled = false;
    renderDash();
    await waitFor(() => expect(screen.getByText("userdashboard.recent_domains")).toBeInTheDocument());
    const meCalls = mockGet.mock.calls.map((c) => String(c[0])).filter((u) => u.startsWith("/me/mailboxes"));
    expect(meCalls).toHaveLength(0);
  });
});

describe("UserDashboard recent mailboxes (JAB-370 Recent projection)", () => {
  beforeEach(() => {
    mailEnabled = true;
    mockGet.mockClear();
  });

  it("loads recent mailboxes with one /me/mailboxes request, never a per-domain fan-out", async () => {
    renderDash();
    await waitFor(() =>
      expect(mockGet).toHaveBeenCalledWith(expect.stringContaining("/me/mailboxes")),
    );
    const urls = mockGet.mock.calls.map((c) => String(c[0]));
    const meCalls = urls.filter((u) => u.startsWith("/me/mailboxes"));
    const perDomainCalls = urls.filter((u) => /\/domains\/[^/]+\/mailboxes/.test(u));

    // Exactly one owner-scoped request, independent of the three owned domains,
    // and no legacy per-domain fan-out.
    expect(meCalls).toHaveLength(1);
    expect(perDomainCalls).toHaveLength(0);

    // The one request asks for the fixed Recent projection: newest-first, capped.
    expect(meCalls[0]).toContain("sort=created_at");
    expect(meCalls[0]).toContain("order=desc");
    expect(meCalls[0]).toContain("page_size=5");
  });

  it("renders the returned rows and the authoritative total from the single envelope", async () => {
    renderDash();
    // Rows come straight from /me/mailboxes (already newest-first, server-sliced).
    await waitFor(() => expect(screen.getByText("newest@one.test")).toBeInTheDocument());
    expect(screen.getByText("older@two.test")).toBeInTheDocument();
    // The stat tile shows the authoritative COUNT across all owned domains (7),
    // not the length of the returned page (2).
    expect(screen.getByText("7")).toBeInTheDocument();
  });
});
