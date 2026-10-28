// BackupSchedulesCard.keepdays.test.tsx — GH #2300.
//
// "Days to keep" keeps every backup made in those days, so the number of
// backups depends on how often the schedule runs. The drawer says about how
// many backups the days keep, caps the days at what the plan's backup limit
// holds at that frequency, and says when the limit is what caps them. Save
// sends keep_within_days and clears the older one-per-day keep_daily.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../apiClient";
import { BackupSchedulesCard } from "./BackupSchedulesCard";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

const VIEW = {
  schedules: [],
  max_backup_schedules: 3,
  max_backups: 7,
  scheduled_backups_enabled: true,
  multi_enabled: true,
  window_start: "02:00",
  window_end: "05:00",
};

beforeEach(() => {
  vi.clearAllMocks();
  mocked.get.mockImplementation(async (url: string) => {
    if (url === "/me/backup-schedules") return { data: { data: VIEW } };
    if (url.startsWith("/me/backups/destinations")) return { data: { data: [], allow_local: false } };
    throw new Error(`unexpected GET ${url}`);
  });
  mocked.post.mockResolvedValue({ data: {} });
});

async function openNew() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <BackupSchedulesCard />
      </App>
    </QueryClientProvider>,
  );
  fireEvent.click(await screen.findByRole("button", { name: /New schedule/ }));
  return (await screen.findByText("New backup schedule")).closest(".ant-drawer") as HTMLElement;
}

async function pickCadence(drawer: HTMLElement, option: string) {
  fireEvent.mouseDown(drawer.querySelector("#cadence") as HTMLElement);
  const opts = await screen.findAllByTitle(option);
  fireEvent.click(opts[opts.length - 1]);
}

describe("BackupSchedulesCard, Days to keep (GH #2300)", () => {
  it("says how many backups the days keep, and when the plan's limit caps them", async () => {
    const drawer = await openNew();
    const days = drawer.querySelector("#keep_within_days") as HTMLInputElement;
    fireEvent.change(days, { target: { value: "3" } });
    expect(await screen.findByText("About 3 backups.")).toBeTruthy();

    // Hourly: a day is 24 backups, more than the plan's 7.
    await pickCadence(drawer, "Hourly");
    expect(await screen.findByText(/About 7 backups: your plan's limit/)).toBeTruthy();
  });

  it("saves the days as keep_within_days and clears keep_daily", async () => {
    const drawer = await openNew();
    fireEvent.change(drawer.querySelector("#keep_within_days") as HTMLInputElement, { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));

    await waitFor(() => expect(mocked.post).toHaveBeenCalled());
    const [url, body] = mocked.post.mock.calls[0];
    expect(url).toBe("/me/backup-schedules");
    expect(body.keep_within_days).toBe(5);
    expect(body.keep_daily).toBeNull();
  });
});
