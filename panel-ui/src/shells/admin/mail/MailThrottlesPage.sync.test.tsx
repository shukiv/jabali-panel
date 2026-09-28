// The throttles table must say whether Stalwart holds what each row asks for.
// A row owns an hourly throttle (stalwart_id) and a daily one
// (stalwart_id_daily); the "Stalwart sync" column used to look only at the
// hourly id, so a daily-only row stayed "pending" forever although its
// throttle was in force.
//
// Deleting a row whose throttle Stalwart cannot remove answers 502 and keeps
// the row (disabled). The page used to swallow that error silently.
//
// Only apiClient and the feedback holder are mocked; the real AntD table,
// dropdown and tags run.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({ apiClient: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } }));
const feedbackMock = vi.hoisted(() => ({
  modal: { confirm: (o: { onOk?: () => void }) => o.onOk?.() },
  message: { success: vi.fn(), error: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({ feedback: feedbackMock }));
import { apiClient } from "../../../apiClient";
import { MailThrottlesPage } from "./MailThrottlesPage";

const mocked = apiClient as unknown as { get: ReturnType<typeof vi.fn>; delete: ReturnType<typeof vi.fn> };

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const baseRow = {
  scope: "global",
  scope_ref: null,
  max_per_hour: 0,
  max_per_day: 0,
  enabled: true,
  stalwart_id: "",
  stalwart_id_daily: "",
  last_applied_at: null,
  last_error: null,
  created_at: "2026-09-29T00:00:00Z",
  updated_at: "2026-09-29T00:00:00Z",
};

function renderPage(items: object[]) {
  mocked.get.mockResolvedValue({ data: { items } });
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <App>
        <MailThrottlesPage />
      </App>
    </QueryClientProvider>,
  );
}

async function syncCell(rowKey: string) {
  const row = await waitFor(() => {
    const r = document.querySelector(`tr[data-row-key="${rowKey}"]`);
    if (!r) throw new Error("row not rendered");
    return r as HTMLElement;
  });
  // columns: Scope, Ref, Per hour, Per day, Enabled, Stalwart sync, Actions
  return row.querySelectorAll("td")[5] as HTMLElement;
}

describe("Stalwart sync column", () => {
  it("shows a daily-only row as synced once its daily throttle exists", async () => {
    renderPage([{ ...baseRow, id: "d1", max_per_day: 1000, stalwart_id_daily: "jg1nyykmahqa" }]);
    const cell = await syncCell("d1");
    expect(within(cell).getByText("synced")).toBeTruthy();
    expect(within(cell).queryByText("pending")).toBeNull();
  });

  it("shows a row as pending while one of its windows has no throttle yet", async () => {
    renderPage([{ ...baseRow, id: "p1", max_per_hour: 100, max_per_day: 1000, stalwart_id: "h1" }]);
    const cell = await syncCell("p1");
    expect(within(cell).getByText("pending")).toBeTruthy();
  });

  it("shows a disabled row whose throttles are gone as synced, not pending", async () => {
    renderPage([{ ...baseRow, id: "x1", max_per_hour: 100, enabled: false }]);
    const cell = await syncCell("x1");
    expect(within(cell).getByText("synced")).toBeTruthy();
  });
});

describe("delete", () => {
  it("shows why Stalwart could not remove the throttle", async () => {
    mocked.delete.mockRejectedValue({
      response: {
        status: 502,
        data: {
          error: "stalwart_delete_failed",
          details: "the throttle is disabled and kept until Stalwart removes it: connection refused",
        },
      },
    });
    renderPage([{ ...baseRow, id: "p1", max_per_hour: 100, stalwart_id: "h1" }]);
    await syncCell("p1");
    fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    fireEvent.click(await screen.findByText("Delete"));
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/admin/mail/throttles/p1"));
    await waitFor(() =>
      expect(feedbackMock.message.error).toHaveBeenCalledWith(
        "the throttle is disabled and kept until Stalwart removes it: connection refused",
      ),
    );
  });
});
