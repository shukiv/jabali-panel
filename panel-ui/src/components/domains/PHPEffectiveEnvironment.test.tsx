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

  it("GH #1701: marks functions this PHP build lacks and explains why", async () => {
    get.mockResolvedValue({
      data: {
        php_version: "8.3",
        pool_found: true,
        disabled_functions: [],
        php_defense: { active: true, mode: "off", pool_rules: false, functions: [] },
        include_path: { value: "", source: "php.ini" },
        session_save_path: { value: "", source: "php.ini" },
        unavailable_functions: ["dl", "pcntl_exec", "pcntl_fork"],
      },
    });
    renderIt();
    fireEvent.click(screen.getByText("Disabled functions and paths"));
    const tags = await screen.findAllByText("Allowed, not in this PHP build");
    expect(tags).toHaveLength(3);
    const dlRow = screen.getByText("dl").closest("tr") as HTMLElement;
    expect(dlRow.textContent).toContain("Allowed, not in this PHP build");
    const execRow = screen.getByText("exec").closest("tr") as HTMLElement;
    expect(execRow.textContent).toContain("Allowed");
    expect(execRow.textContent).not.toContain("not in this PHP build");
    expect(screen.getByText(/PHP 8.3 for websites \(PHP-FPM\) does not include it/)).toBeTruthy();
  });

  it("GH #2001: says an enforced AppArmor profile lets program functions start only the shell and cat", async () => {
    get.mockResolvedValue({
      data: {
        php_version: "8.4",
        pool_found: true,
        disabled_functions: [{ name: "exec", source: "pool" }],
        php_defense: { active: false, mode: "", pool_rules: false, functions: [] },
        include_path: { value: "", source: "php.ini" },
        session_save_path: { value: "", source: "php.ini" },
        unavailable_functions: [],
        exec_confined: true,
        exec_confinement: "enforce",
      },
    });
    renderIt();
    fireEvent.click(screen.getByText("Disabled functions and paths"));
    const shellRow = (await screen.findByText("shell_exec")).closest("tr") as HTMLElement;
    expect(shellRow.textContent).toContain("Allowed");
    expect(shellRow.textContent).toContain("Starts only the shell and cat");
    const execRow = screen.getByText("exec").closest("tr") as HTMLElement;
    expect(execRow.textContent).not.toContain("Starts only the shell and cat");
    expect(screen.getByText(/Commands\s+such as df, ls, grep or id fail/)).toBeTruthy();
  });

  it("GH #2001: no confinement tag when the agent does not say", async () => {
    renderIt();
    fireEvent.click(screen.getByText("Disabled functions and paths"));
    await screen.findByText("shell_exec");
    expect(screen.queryByText("Starts only the shell and cat")).toBeNull();
    expect(screen.queryByText("Can start any program")).toBeNull();
  });

  it("GH #2001: says program functions can start anything when the profile only logs", async () => {
    get.mockResolvedValue({
      data: {
        php_version: "8.4",
        pool_found: true,
        disabled_functions: [{ name: "exec", source: "pool" }],
        php_defense: { active: false, mode: "", pool_rules: false, functions: [] },
        include_path: { value: "", source: "php.ini" },
        session_save_path: { value: "", source: "php.ini" },
        unavailable_functions: [],
        exec_confined: false,
        exec_confinement: "complain",
      },
    });
    renderIt();
    fireEvent.click(screen.getByText("Disabled functions and paths"));
    const shellRow = (await screen.findByText("shell_exec")).closest("tr") as HTMLElement;
    expect(shellRow.textContent).toContain("Can start any program");
    expect(shellRow.textContent).not.toContain("Starts only the shell and cat");
    expect((screen.getByText("exec").closest("tr") as HTMLElement).textContent).not.toContain("Can start any program");
    expect(screen.getByText(/is in complain mode, so it only logs/)).toBeTruthy();
    expect(screen.getByText(/the same as shell access/)).toBeTruthy();
  });

  it("GH #1701: says when the build check could not run", async () => {
    get.mockResolvedValue({
      data: {
        php_version: "8.3",
        pool_found: true,
        disabled_functions: [],
        php_defense: { active: false, mode: "", pool_rules: false, functions: [] },
        include_path: { value: "", source: "php.ini" },
        session_save_path: { value: "", source: "php.ini" },
        unavailable_functions: [],
        availability_error: "php-fpm 8.3 module list failed",
      },
    });
    renderIt();
    fireEvent.click(screen.getByText("Disabled functions and paths"));
    expect(await screen.findByText(/Could not check which functions this PHP build provides/)).toBeTruthy();
    expect(screen.queryByText("Allowed, not in this PHP build")).toBeNull();
  });
});
