// GH #2224: the egress drawer lists what a learning policy let through
// (and enforced mode would block) next to what was dropped, and says which.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ apiClient: { get: vi.fn(), put: vi.fn() } }));
vi.mock("../../../apiClient", () => api);

import { DropEventsDrawer, type EgressDropEvent } from "./AdminSecurityEgress";

const events: EgressDropEvent[] = [
  { dest: "5.6.7.8", port: 8443, proto: "TCP", mode: "learning", count: 2, last_seen: "2026-10-22T10:03:00+0000" },
  { dest: "1.2.3.4", port: 3306, proto: "TCP", mode: "enforced", count: 1, last_seen: "2026-10-22T10:00:00+0000" },
  // An agent from before GH #2224 sends no mode: those lines were drops.
  { dest: "9.9.9.9", port: 25, proto: "TCP", count: 1, last_seen: "2026-10-22T09:00:00+0000" },
];

function rowFor(dest: string) {
  const cell = screen.getByText(dest);
  const row = cell.closest("tr");
  if (!row) throw new Error(`no row for ${dest}`);
  return within(row);
}

describe("DropEventsDrawer", () => {
  it("labels learning flows as allowed and enforced ones as blocked", async () => {
    api.apiClient.get.mockResolvedValue({ data: { source: "nftables egress log", events } });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <AntApp>
          <DropEventsDrawer userID="u1" open onClose={() => {}} />
        </AntApp>
      </QueryClientProvider>,
    );
    expect(await screen.findByText("5.6.7.8")).toBeTruthy();
    expect(api.apiClient.get).toHaveBeenCalledWith("/admin/users/u1/egress/drop-events");
    expect(rowFor("5.6.7.8").getByText("Allowed (learning)")).toBeTruthy();
    expect(rowFor("5.6.7.8").queryByText("Blocked")).toBeNull();
    expect(rowFor("1.2.3.4").getByText("Blocked")).toBeTruthy();
    expect(rowFor("9.9.9.9").getByText("Blocked")).toBeTruthy();
  });
});
