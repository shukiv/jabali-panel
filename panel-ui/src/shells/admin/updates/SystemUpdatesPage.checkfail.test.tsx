// SystemUpdatesPage.checkfail.test.tsx — GH #2467. When the panel version
// check failed, the Jabali Panel card showed "—" for both versions with
// "Up to date" and "No updates available", so a broken check read as a
// missing version. It now says the check failed.
import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

type MasonryItem = { key: string; children?: ReactNode };

vi.mock("antd", async (importOriginal) => {
  const actual = await importOriginal<typeof import("antd")>();
  return {
    ...actual,
    // happy-dom has no layout; render the cards in a plain list.
    Masonry: ({ items = [] }: { items?: MasonryItem[] }) => (
      <div>{items.map((it) => <div key={it.key}>{it.children}</div>)}</div>
    ),
  };
});

const check = vi.hoisted(() => ({ fail: false }));

vi.mock("../../../apiClient", () => ({
  apiClient: {
    get: vi.fn((url: string) => {
      if (url === "/admin/updates/jabali/check") {
        if (check.fail) return Promise.reject(new Error("Agent error"));
        return Promise.resolve({
          data: { current_sha: "84018a0d6aaaa", remote_sha: "84018a0d6aaaa", behind_count: 0, branch: "main" },
        });
      }
      if (url === "/admin/updates/state") {
        return Promise.resolve({
          data: { jabali_behind: 0, jabali_current_sha: "84018a0d6aaaa", apt_total: 0, apt_security: 0 },
        });
      }
      if (url.includes("/admin/updates/history")) return Promise.resolve({ data: { items: [], total: 0 } });
      if (url === "/admin/updates/apt/check") return Promise.resolve({ data: { packages: [], total: 0 } });
      if (url === "/users") return Promise.resolve({ data: { data: [] } });
      return Promise.resolve({ data: {} });
    }),
    post: vi.fn(() => Promise.resolve({ data: {} })),
    put: vi.fn(() => Promise.resolve({ data: {} })),
    patch: vi.fn(() => Promise.resolve({ data: {} })),
  },
}));

vi.mock("./RepairCard", () => ({ RepairCard: () => <div>Repair Center</div> }));
vi.mock("./DomainRepairCard", () => ({ DomainRepairCard: () => <div>Domain repair</div> }));
vi.mock("./DNSOrphanRepairCard", () => ({ DNSOrphanRepairCard: () => <div>PowerDNS orphan records</div> }));

import { SystemUpdatesPage } from "./SystemUpdatesPage";

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <SystemUpdatesPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  check.fail = false;
});

describe("SystemUpdatesPage panel version check (GH #2467)", () => {
  it("says the check failed instead of showing up to date", async () => {
    check.fail = true;
    renderPage();

    expect(await screen.findByText("Couldn't check for panel updates")).toBeTruthy();
    expect(screen.getByText(/Agent error\. The panel log has the details\./)).toBeTruthy();
    // Both the Panel Version card and the Jabali Panel card.
    expect(screen.getAllByText("Check failed")).toHaveLength(2);
    expect(screen.queryByText("No updates available")).toBeNull();
  });

  it("shows the versions and no error when the check works", async () => {
    renderPage();

    expect(await screen.findByText("No updates available")).toBeTruthy();
    expect(screen.getAllByText("84018a0d").length).toBeGreaterThanOrEqual(2);
    expect(screen.queryByText("Couldn't check for panel updates")).toBeNull();
    expect(screen.queryByText("Check failed")).toBeNull();
  });
});
