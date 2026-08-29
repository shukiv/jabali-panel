// GH #1332 item 4: the Performance card must drive off the caller's per-version
// pools (GET /me/php-pool-tuning) — showing each version's own tuning — and
// version-scope its writes, instead of the old static-defaults / default-pool
// behaviour.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { UserPHPPerformanceCard } from "./UserPHPPerformanceCard";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
};

function pool(v: string, isDefault: boolean, maxChildren: number, perf: string) {
  return {
    php_version: v,
    pm_mode: "dynamic",
    pm_max_children: maxChildren,
    pm_start_servers: 2,
    pm_min_spare_servers: 1,
    pm_max_spare_servers: 3,
    pm_max_requests: 0,
    request_terminate_timeout_seconds: 0,
    process_idle_timeout_seconds: 60,
    performance_mode: perf,
    is_default: isDefault,
  };
}

function mockTuning() {
  mocked.get.mockReset().mockResolvedValue({
    data: {
      can_edit: true,
      advanced: true,
      max_children_cap: 20,
      worker_mem_mb: 128,
      modes: [
        { mode: "balanced", label: "Balanced" },
        { mode: "custom", label: "Custom" },
      ],
      pools: [pool("8.4", true, 5, "balanced"), pool("8.5", false, 15, "custom")],
    },
  });
}

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <UserPHPPerformanceCard />
      </App>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mockTuning();
  mocked.put.mockReset().mockResolvedValue({ data: {} });
});

describe("GH #1332 — UserPHPPerformanceCard per-version tuning", () => {
  it("loads the per-version pools endpoint and lists each version", async () => {
    renderCard();
    await waitFor(() =>
      expect(mocked.get).toHaveBeenCalledWith("/me/php-pool-tuning"),
    );
    // The old policy-only endpoint is no longer the driver.
    expect(mocked.get).not.toHaveBeenCalledWith("/me/php-fpm-policy");
    // The version selector renders (2 pools) with the default preselected, and
    // the per-version helper text is shown.
    await screen.findByText("PHP 8.4 (default)");
    await screen.findByText("Each PHP version keeps its own tuning.");
  });

  it("version-scopes the performance-mode write to the selected (default) version", async () => {
    renderCard();
    // Wait for the pools to load (default selector resolves to 8.4).
    await screen.findByText("PHP 8.4 (default)");
    const applyMode = await screen.findByRole("button", { name: /apply mode/i });
    fireEvent.click(applyMode);
    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/me/php-performance-mode", {
        php_version: "8.4",
        mode: "balanced",
      }),
    );
  });
});
