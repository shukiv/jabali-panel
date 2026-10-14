// SystemUpdatesPage.layout.test.tsx — the Updates page lays its cards out in
// two independently stacked columns. Rows of Col pairs left a hole under the
// shorter card of each pair (Repair Center beside the taller Domain repair).
// happy-dom has no layout, so this pins the Masonry contract (each card's
// column, the order, re-measuring on resize); the gaps themselves were checked
// in a real browser.
import type { ReactNode } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";

type MasonryItem = { key: string; column?: number; children?: ReactNode };
type MasonryProps = { items?: MasonryItem[]; fresh?: boolean; columns?: unknown };

vi.mock("antd", async (importOriginal) => {
  const actual = await importOriginal<typeof import("antd")>();
  return {
    ...actual,
    Masonry: ({ items = [], fresh, columns }: MasonryProps) => (
      <div data-testid="masonry" data-fresh={String(!!fresh)} data-columns={JSON.stringify(columns)}>
        {items.map((it) => (
          <div key={it.key} data-testid={`item-${it.key}`} data-column={String(it.column)}>
            {it.children}
          </div>
        ))}
      </div>
    ),
  };
});

vi.mock("../../../apiClient", () => ({
  apiClient: {
    get: vi.fn((url: string) => {
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

describe("SystemUpdatesPage layout", () => {
  it("stacks the cards in two columns that keep their cards", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <SystemUpdatesPage />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const masonry = await screen.findByTestId("masonry");

    // Re-measure when a card grows (a scan result), or it overlaps the next.
    expect(masonry).toHaveAttribute("data-fresh", "true");
    // Two columns from xl, one below — the breakpoint the Col grid used.
    expect(JSON.parse(masonry.getAttribute("data-columns") ?? "null")).toEqual({ xs: 1, xl: 2 });

    const items = Array.from(masonry.querySelectorAll<HTMLElement>("[data-testid^='item-']")).map((el) => [
      el.dataset.testid!.slice("item-".length),
      el.dataset.column,
    ]);
    // Order is the single-column order; each card is pinned to its side.
    expect(items).toEqual([
      ["jabali", "0"],
      ["apt", "1"],
      ["repair", "0"],
      ["domain-repair", "1"],
      ["dns-orphans", "0"],
      ["autoupdate", "0"],
      ["history", "1"],
    ]);
    await waitFor(() => expect(screen.getByTestId("item-repair")).toHaveTextContent("Repair Center"));
    expect(screen.getByTestId("item-domain-repair")).toHaveTextContent("Domain repair");
    expect(screen.getByTestId("item-dns-orphans")).toHaveTextContent("PowerDNS orphan records");
  });
});
