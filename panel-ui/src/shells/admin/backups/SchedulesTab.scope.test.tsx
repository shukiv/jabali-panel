// SchedulesTab.scope.test.tsx — regression for GH #502.
//
// The reporter could create scheduled backups for "all users + system"
// only: the schedule drawer hard-coded kind=account_backup and forced a
// user pick, so there was no way to schedule a System/Panel-only or an
// explicit Full-Server backup — even though the backend + scheduler
// already support both.
//
// These tests lock the drawer's Type -> wire mapping:
//   System      -> POST {kind:"system_backup"}          (no user fan-out)
//   Full Server -> POST {kind:"account_backup", user_ids:[],
//                        include_system_backup:true}     (all accounts + system)
// and that picking "System" hides the account-only Users field.
// Only apiClient is mocked; AntD Form/Drawer run for real.
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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
};

// SchedulesTab loads schedules + destinations + users on mount.
function mockLists() {
  mocked.get.mockImplementation(async (url: string) => {
    if (url.startsWith("/admin/backup-schedules")) {
      return { data: { data: [] } };
    }
    if (url.includes("/backup-destinations")) {
      return {
        data: { data: [{ id: "d1", name: "off-site", kind: "sftp", enabled: true }] },
      };
    }
    if (url.startsWith("/users")) {
      return {
        data: {
          data: [{ id: "u1", username: "alice", email: "alice@example.com", is_admin: false }],
        },
      };
    }
    throw new Error(`unexpected GET ${url}`);
  });
}

// openDrawerAndPickDest opens "New schedule" and selects the one
// destination (required for save), returning once the drawer is up.
async function openDrawer() {
  render(<SchedulesTab />);
  // "New schedule" is disabled until at least one destination loads.
  const btn = await screen.findByRole("button", { name: /new schedule/i });
  await waitFor(() => expect(btn).not.toBeDisabled());
  fireEvent.click(btn);
  // The drawer's Type radios confirm it opened (avoid matching the
  // table's "Type" column header, which shares the text).
  await screen.findByRole("radio", { name: "Full Server" });
}

async function pickDestination() {
  // The Destinations Select is a multi-select; open it and click the option.
  const dest = document.querySelector<HTMLInputElement>("#destination_ids");
  expect(dest).toBeTruthy();
  fireEvent.mouseDown(dest as HTMLElement);
  fireEvent.click(await screen.findByText(/off-site \(sftp\)/i));
}

beforeEach(() => {
  vi.clearAllMocks();
  mockLists();
  mocked.post.mockResolvedValue({ data: {} });
});

describe("SchedulesTab scope (GH #502)", () => {
  it("System scope hides the Users field and posts kind=system_backup", async () => {
    await openDrawer();

    // Account is the default: Users field is present.
    expect(screen.getByLabelText("Users")).toBeTruthy();

    fireEvent.click(screen.getByRole("radio", { name: "System" }));

    // Users field is gone in System scope.
    await waitFor(() => expect(screen.queryByLabelText("Users")).toBeNull());

    await pickDestination();
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));

    await waitFor(() => expect(mocked.post).toHaveBeenCalled());
    const [url, body] = mocked.post.mock.calls[0];
    expect(url).toBe("/admin/backup-schedules");
    expect(body.kind).toBe("system_backup");
  });

  it("Full Server scope posts account_backup + all users + include_system_backup", async () => {
    await openDrawer();
    fireEvent.click(screen.getByRole("radio", { name: "Full Server" }));

    // No account-only Users picker in Full Server scope.
    await waitFor(() => expect(screen.queryByLabelText("Users")).toBeNull());

    await pickDestination();
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));

    await waitFor(() => expect(mocked.post).toHaveBeenCalled());
    const [, body] = mocked.post.mock.calls[0];
    expect(body.kind).toBe("account_backup");
    expect(body.user_ids).toEqual([]); // empty = all non-admin users
    expect(body.include_system_backup).toBe(true);
  });
});
