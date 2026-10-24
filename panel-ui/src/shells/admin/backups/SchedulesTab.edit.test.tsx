// SchedulesTab.edit.test.tsx — regression for GH #2246.
//
// A schedule an account made on its own Backups page runs on an interval
// (cadence) and backs up what the account chose (content); it has no cron
// expression. The admin Schedules tab didn't know either field: Edit opened it
// as "Accounts, Daily at 03:00" (the form's fallback for a cron it can't read)
// and Save sent that cron time, which moved the schedule's next run.
//
// These tests lock that such a schedule shows and edits its own settings with
// the account's choices, that Save sends no cron, users or system flag, and
// that an existing schedule can't be switched to another kind (System on an
// account schedule saved it with an empty user list = every account).
// Only apiClient is mocked; AntD Form/Drawer run for real.
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";
import { SchedulesTab } from "./SchedulesTab";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
};

const DEST = { id: "d1", name: "off-site", kind: "sftp", enabled: true };

// What the admin API returns for a schedule alice made on her Backups page.
const TENANT_ROW = {
  id: "s-tenant",
  kind: "account_backup",
  user_id: "u1",
  user_ids: ["u1"],
  include_system_backup: false,
  cron_expr: "",
  content: "database",
  cadence: "every_6h",
  enabled: true,
  keep_daily: 3,
  destinations: [DEST],
};

const ADMIN_ROW = {
  id: "s-admin",
  kind: "account_backup",
  user_ids: [],
  include_system_backup: false,
  cron_expr: "30 2 * * *",
  content: "full",
  cadence: "",
  enabled: true,
  destinations: [DEST],
};

const SYSTEM_ROW = { ...ADMIN_ROW, id: "s-system", kind: "system_backup" };

function mockLists(rows: unknown[]) {
  mocked.get.mockImplementation(async (url: string) => {
    if (url.startsWith("/admin/backup-schedules")) return { data: { data: rows } };
    if (url.includes("/backup-destinations")) return { data: { data: [DEST] } };
    if (url.startsWith("/users")) {
      return {
        data: { data: [{ id: "u1", username: "alice", email: "alice@example.com", is_admin: false }] },
      };
    }
    throw new Error(`unexpected GET ${url}`);
  });
}

// openEdit clicks Edit in the row's overflow menu and returns the drawer.
async function openEdit(rowText: string | RegExp) {
  render(<SchedulesTab />);
  const cell = await screen.findByText(rowText);
  const row = cell.closest("tr") as HTMLElement;
  fireEvent.click(within(row).getByRole("button", { name: "More actions" }));
  fireEvent.click(await screen.findByText("Edit"));
  return (await screen.findByText("Edit schedule")).closest(".ant-drawer") as HTMLElement;
}

async function pick(drawer: HTMLElement, fieldID: string, option: string) {
  const input = drawer.querySelector<HTMLInputElement>(`#${fieldID}`);
  expect(input).toBeTruthy();
  fireEvent.mouseDown(input as HTMLElement);
  const opts = await screen.findAllByTitle(option);
  fireEvent.click(opts[opts.length - 1]);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocked.post.mockResolvedValue({ data: {} });
  mocked.patch.mockResolvedValue({ data: {} });
});

describe("SchedulesTab, an account's own schedule (GH #2246)", () => {
  it("lists its type, interval and account", async () => {
    mockLists([TENANT_ROW]);
    render(<SchedulesTab />);
    const row = (await screen.findByText("Every 6 hours")).closest("tr") as HTMLElement;
    expect(within(row).getByText("Databases only")).toBeTruthy();
    expect(within(row).getByText("Account's own")).toBeTruthy();
    expect(within(row).getByText("alice")).toBeTruthy();
  });

  it("an old single-user admin schedule widened to more users lists all of them", async () => {
    // user_id from before user_ids, then more users added: not the account's own.
    mockLists([{ ...ADMIN_ROW, user_id: "u1", user_ids: ["u1", "u2"] }]);
    render(<SchedulesTab />);
    const row = (await screen.findByText("30 2 * * *")).closest("tr") as HTMLElement;
    expect(within(row).getByText("2 selected")).toBeTruthy();
    expect(within(row).queryByText("Account's own")).toBeNull();
  });

  it("opens with the account's settings and saves only those", async () => {
    mockLists([TENANT_ROW]);
    const drawer = await openEdit("Every 6 hours");

    // The account's own choices, not the cron form's "Daily 03:00".
    expect(within(drawer).getByText("alice (alice@example.com)")).toBeTruthy();
    expect(within(drawer).getByTitle("Databases only")).toBeTruthy();
    expect(within(drawer).getByTitle("Every 6 hours")).toBeTruthy();
    expect(within(drawer).queryByText("Frequency")).toBeNull();
    expect(within(drawer).queryByText("Time")).toBeNull();
    expect(within(drawer).queryByLabelText("Users")).toBeNull();
    expect(within(drawer).queryByText("Include system backup")).toBeNull();

    await pick(drawer, "cadence", "Hourly");
    await pick(drawer, "content", "Files only");
    fireEvent.click(within(drawer).getByRole("button", { name: /^Save$/ }));

    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    const [url, body] = mocked.patch.mock.calls[0];
    expect(url).toBe("/admin/backup-schedules/s-tenant");
    expect(body).toEqual({
      content: "files",
      cadence: "hourly",
      destination_ids: ["d1"],
      keep_daily: 3,
      enabled: true,
    });
  });
});

describe("SchedulesTab, editing keeps a schedule's kind (GH #2246)", () => {
  it("an account schedule can't become System", async () => {
    mockLists([ADMIN_ROW]);
    const drawer = await openEdit("30 2 * * *");
    expect(within(drawer).getByRole("radio", { name: "System" })).toBeDisabled();
    expect(within(drawer).getByRole("radio", { name: "Accounts" })).not.toBeDisabled();
    expect(within(drawer).getByRole("radio", { name: "Full Server" })).not.toBeDisabled();
  });

  it("a System schedule can't become an account schedule", async () => {
    mockLists([SYSTEM_ROW]);
    const drawer = await openEdit("30 2 * * *");
    expect(within(drawer).getByRole("radio", { name: "System" })).not.toBeDisabled();
    expect(within(drawer).getByRole("radio", { name: "Accounts" })).toBeDisabled();
    expect(within(drawer).getByRole("radio", { name: "Full Server" })).toBeDisabled();
  });

  it("an admin account schedule saves what it backs up", async () => {
    mockLists([ADMIN_ROW]);
    const drawer = await openEdit("30 2 * * *");
    await pick(drawer, "content", "Databases only");
    fireEvent.click(within(drawer).getByRole("button", { name: /^Save$/ }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    const [, body] = mocked.patch.mock.calls[0];
    expect(body.content).toBe("database");
    expect(body.cron_expr).toBe("30 2 * * *");
    expect(body.user_ids).toEqual([]);
  });

  it("a single-account cron schedule keeps its account", async () => {
    // An account's own schedule from before the interval list: cron-timed.
    mockLists([{ ...TENANT_ROW, cadence: "", cron_expr: "0 4 * * *" }]);
    const drawer = await openEdit("0 4 * * *");
    expect(within(drawer).getByText("alice (alice@example.com)")).toBeTruthy();
    expect(within(drawer).queryByLabelText("Users")).toBeNull();
    expect(within(drawer).getByRole("radio", { name: "Full Server" })).toBeDisabled();
    fireEvent.click(within(drawer).getByRole("button", { name: /^Save$/ }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    const [, body] = mocked.patch.mock.calls[0];
    expect(body.cron_expr).toBe("0 4 * * *");
    expect(body.content).toBe("database");
    expect(body).not.toHaveProperty("user_ids");
    expect(body).not.toHaveProperty("include_system_backup");
  });
});
