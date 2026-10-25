// AdminSecuritySnuffleupagus.incidents.test.tsx — GH #2262. Recent incidents
// pages through the server (it got one page with the full total, so every
// page showed the same 50 rows), a row opens the triage drawer, and Clear
// history deletes the history after a confirmation.
// apiClient and the toasts are mocked; the real AntD table and drawer render.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AdminSecuritySnuffleupagus } from "./AdminSecuritySnuffleupagus";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn(), success: vi.fn() }, modal: { confirm: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};
const confirm = feedback.modal.confirm as unknown as ReturnType<typeof vi.fn>;

const TOTAL = 120;

function incident(id: number) {
  return {
    id,
    ts: "2026-10-24T10:00:00Z",
    rule_name: `sp.disabled_function:rule${id}`,
    action: "block",
    source_ip: "203.0.113.9",
    request_uri: `/x${id}.php`,
    domain: "shop.example.com",
    php_version: "8.4",
  };
}

function incidentsPage(url: string) {
  const params = new URLSearchParams(url.split("?")[1] ?? "");
  const page = Number(params.get("page") ?? "1");
  const size = Number(params.get("limit") ?? "50");
  const first = (page - 1) * size + 1;
  const ids = Array.from({ length: Math.max(0, Math.min(size, TOTAL - first + 1)) }, (_, i) => first + i);
  return { data: ids.map(incident), total: TOTAL, page, page_size: size };
}

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <App>
        <AdminSecuritySnuffleupagus />
      </App>
    </QueryClientProvider>,
  );
}

describe("PHP Defense recent incidents (GH #2262)", () => {
  beforeEach(() => {
    confirm.mockReset();
    mocked.delete.mockReset().mockResolvedValue({ data: { deleted: TOTAL } });
    mocked.get.mockReset().mockImplementation(async (url: string) => {
      if (url.includes("/status")) return { data: { enabled: true, mode: "simulation", php_versions_loaded: [] } };
      if (url.includes("/rules")) return { data: { rules: [] } };
      if (url.includes("/incidents")) return { data: incidentsPage(url) };
      throw new Error(`unexpected GET ${url}`);
    });
  });

  it("asks the server for the page you open", async () => {
    renderPage();
    expect(await screen.findByText("sp.disabled_function:rule1")).toBeTruthy();

    fireEvent.click(screen.getByTitle("2"));

    expect(await screen.findByText("sp.disabled_function:rule51")).toBeTruthy();
    expect(screen.queryByText("sp.disabled_function:rule1")).toBeNull();
    expect(mocked.get.mock.calls.some(([u]) => String(u).includes("page=2"))).toBe(true);
  });

  it("opens the incident drawer from a row", async () => {
    renderPage();
    fireEvent.click(await screen.findByText("sp.disabled_function:rule3"));

    const drawer = await screen.findByRole("dialog");
    expect(within(drawer).getByText("Incident #3")).toBeTruthy();
    expect(within(drawer).getAllByText("shop.example.com").length).toBeGreaterThan(0);
  });

  it("clears the history after a confirmation that names the count", async () => {
    renderPage();
    await screen.findByText("sp.disabled_function:rule1");

    fireEvent.click(screen.getByRole("button", { name: "Clear history" }));
    expect(confirm).toHaveBeenCalledTimes(1);
    const opts = confirm.mock.calls[0][0] as { content: string; okType: string; onOk: () => Promise<void> };
    expect(opts.content).toContain(`all ${TOTAL} recorded incidents`);
    expect(opts.okType).toBe("danger");
    expect(mocked.delete).not.toHaveBeenCalled();

    await opts.onOk();
    await waitFor(() =>
      expect(mocked.delete).toHaveBeenCalledWith("/admin/security/snuffleupagus/incidents"),
    );
  });
});
