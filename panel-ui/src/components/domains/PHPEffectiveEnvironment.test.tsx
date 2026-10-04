// GH #1701: the read-only PHP environment on a domain's PHP Settings page
// fetches only when opened, and shows the pool's real state.
import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("../../apiClient", () => ({ apiClient: { get: vi.fn() } }));

import { apiClient } from "../../apiClient";
import { PHPEffectiveEnvironment } from "./PHPEffectiveEnvironment";

const get = (apiClient as unknown as { get: ReturnType<typeof vi.fn> }).get;

function renderIt() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PHPEffectiveEnvironment domainId="d1" />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  get.mockResolvedValue({
    data: {
      php_version: "8.4",
      pool_found: true,
      disabled_functions: [{ name: "exec", source: "pool" }],
      php_defense: { active: true, mode: "enforce", pool_rules: true, functions: [{ name: "system", state: "blocked" }] },
      include_path: { value: ".:/usr/share/php", source: "php.ini" },
      session_save_path: { value: "/home/u1/.sessions", source: "pool" },
    },
  });
});

describe("PHPEffectiveEnvironment", () => {
  it("does not fetch until opened, then shows the pool's functions and paths", async () => {
    renderIt();
    expect(get).not.toHaveBeenCalled();
    fireEvent.click(screen.getByText("Disabled functions and paths"));
    expect(await screen.findByText("Disabled by the hosting package")).toBeTruthy();
    expect(get).toHaveBeenCalledWith("/domains/d1/php-settings/effective");
    expect(screen.getByText("Blocked by PHP Defense")).toBeTruthy();
    // shell_exec is neither disabled nor banned here.
    expect(screen.getAllByText("Allowed").length).toBeGreaterThan(0);
    expect(screen.getByText(/lifts its ban on the functions the package allows/)).toBeTruthy();
    expect(screen.getByText("/home/u1/.sessions")).toBeTruthy();
    expect(screen.getByText("PHP pool setting")).toBeTruthy();
  });

  it("says when the pool is not set up yet", async () => {
    get.mockResolvedValue({ data: { pool_found: false } });
    renderIt();
    fireEvent.click(screen.getByText("Disabled functions and paths"));
    expect(await screen.findByText("This domain's PHP pool has not been set up yet.")).toBeTruthy();
  });
});
