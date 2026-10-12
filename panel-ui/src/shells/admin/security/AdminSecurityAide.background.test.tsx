// AdminSecurityAide.background.test.tsx — GH #2079. The baseline rebuild and
// the check run in the background; the page says what is running and why the
// last rebuild failed. A failed start used to say only "Rebuild failed — see
// agent logs", and the agent logs didn't say why either.
// apiClient and the toast are mocked; the real AntD components render.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AdminSecurityAide } from "./AdminSecurityAide";
import type { AideStatus } from "../../../hooks/useSecurityAide";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};
const toastError = feedback.message.error as unknown as ReturnType<typeof vi.fn>;
const toastInfo = feedback.message.info as unknown as ReturnType<typeof vi.fn>;

const base: AideStatus = {
  enabled: true,
  db_age_seconds: 86_400,
  last_check_ts: "2026-10-12 04:30:01 +0000",
  summary: { added: 1200, changed: 1100, removed: 100 },
  sample: [],
};

function renderPage(status: AideStatus) {
  mocked.get.mockResolvedValue({ data: status });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <App>
        <AdminSecurityAide />
      </App>
    </QueryClientProvider>,
  );
}

function rebuildButton() {
  return screen.getByRole("button", { name: /Rebuild baseline/ });
}

describe("AIDE rebuild and check in the background (GH #2079)", () => {
  beforeEach(() => {
    mocked.get.mockReset();
    mocked.post.mockReset();
    toastError.mockReset();
    toastInfo.mockReset();
  });

  it("says a rebuild is running and won't start another", async () => {
    renderPage({ ...base, rebuild: { running: true, started_at: "2026-10-12T14:00:00Z" } });
    expect(await screen.findByText("Rebuilding the baseline…")).toBeTruthy();
    expect(rebuildButton().className).toContain("ant-btn-loading");
    // A check against the baseline being replaced would mislead.
    expect(screen.getByRole("button", { name: /Run check now/ }).hasAttribute("disabled")).toBe(true);
  });

  it("shows why the last rebuild failed", async () => {
    const reason = "aide --init failed (exit status 17): ERROR: /etc/aide/aide.conf:11: open (read/write) failed for file '/var/log/aide/aide.report.log': Permission denied";
    renderPage({ ...base, rebuild: { running: false, ok: false, error: reason } });
    expect(await screen.findByText("The last baseline rebuild failed")).toBeTruthy();
    expect(screen.getByText(reason)).toBeTruthy();
  });

  it("shows a failed rebuild on a host whose database is missing", async () => {
    renderPage({ ...base, enabled: false, reason: "AIDE DB missing — run 'aide --init'", rebuild: { running: false, ok: false, error: "aide --init failed: ERROR: disk full" } });
    expect(await screen.findByText("aide --init failed: ERROR: disk full")).toBeTruthy();
  });

  it("says when the baseline was rebuilt and no check has run since", async () => {
    renderPage({
      ...base,
      last_check_ts: undefined,
      baseline_rebuilt_at: "2026-10-12 14:02:00 +0000",
      summary: { added: 0, changed: 0, removed: 0 },
      rebuild: { running: false, ok: true },
    });
    expect(await screen.findByText("The baseline was rebuilt")).toBeTruthy();
    // The rebuild wasn't a check: the page must not say one found nothing.
    expect(screen.queryByText("adminsecurityaide.no_tamper_detected")).toBeNull();
    expect(screen.queryByText(/No tamper detected/i)).toBeNull();
  });

  it("toasts that the rebuild started, and the server's reason when it didn't", async () => {
    renderPage(base);
    mocked.post.mockResolvedValueOnce({ data: { started: true } });
    fireEvent.click(await screen.findByRole("button", { name: /Rebuild baseline/ }));
    const ok = document.querySelector(".ant-popconfirm .ant-btn-dangerous") as HTMLElement;
    fireEvent.click(ok);
    await waitFor(() => expect(toastInfo).toHaveBeenCalled());
    expect(mocked.post).toHaveBeenCalledWith("/admin/security/aide/rebuild", { dry_run: false, confirm: "REBUILD" });
    expect(String(toastInfo.mock.calls[0][0])).toMatch(/Rebuild started/);

    mocked.post.mockRejectedValueOnce({
      response: { status: 409, data: { status: "error", error: "failed_precondition", detail: "a baseline rebuild is already running" } },
      message: "Request failed with status code 409",
    });
    fireEvent.click(rebuildButton());
    const oks = document.querySelectorAll(".ant-popconfirm .ant-btn-dangerous");
    fireEvent.click(oks[oks.length - 1] as HTMLElement);
    await waitFor(() => expect(toastError).toHaveBeenCalled());
    expect(String(toastError.mock.calls[0][0])).toContain("a baseline rebuild is already running");
    expect(String(toastError.mock.calls[0][0])).not.toMatch(/agent logs/);
  });

  it("toasts that the check started", async () => {
    renderPage(base);
    mocked.post.mockResolvedValueOnce({ data: { started: true } });
    fireEvent.click(await screen.findByRole("button", { name: /Run check now/ }));
    await waitFor(() => expect(toastInfo).toHaveBeenCalled());
    expect(mocked.post).toHaveBeenCalledWith("/admin/security/aide/check");
    expect(String(toastInfo.mock.calls[0][0])).toMatch(/Check started/);
  });

  it("says a check is running", async () => {
    renderPage({ ...base, check: { running: true } });
    expect(await screen.findByText("Checking the files against the baseline…")).toBeTruthy();
  });
});
