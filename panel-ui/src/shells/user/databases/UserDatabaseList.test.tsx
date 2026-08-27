// UserDatabaseList.test.tsx — GH #1045. Locks the per-database
// Backup/Restore row actions: "Download backup" fetches the dump via
// the apiClient helper and triggers a save-as with the server-provided
// filename; "Restore from file" confirms first, then uploads the picked
// .sql file to the restore endpoint. Data/URL/mutation hooks are
// mocked; the real AntD table + RowActions menu run so a UI break
// surfaces here.
import { App } from "antd";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { UserDatabaseList } from "./UserDatabaseList";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("../../../apiClient", () => ({
  downloadDatabaseBackup: vi.fn(),
  restoreDatabaseUploadAuto: vi.fn(),
  ssoAdminer: vi.fn(),
  ssoPhpMyAdmin: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));

// getIdentity pulls in identity.ts → query.ts (which instantiates a real
// QueryClient); with react-query mocked above that import throws at load. Mock
// it directly and hand the restore-size pre-check (GH #1044) a cap.
vi.mock("../../../identity", () => ({
  getIdentity: () => Promise.resolve({ uploadMaxSizeMb: 1024 }),
}));

vi.mock("../../../hooks/useQueries", () => ({
  useDeleteMutation: () => ({ mutateAsync: vi.fn() }),
  useCreateMutation: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

// Hoisted mutable holder so a test can swap the table's rows (e.g. to a
// Postgres row) before render. Defaults to a single MariaDB row.
const { rowsHolder, mariaRow } = vi.hoisted(() => {
  const mariaRow = {
    id: "db1",
    user_id: "u1",
    name: "shop_db",
    engine: "mariadb",
    charset: "utf8mb4",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
  };
  return { rowsHolder: { rows: [mariaRow] as Array<typeof mariaRow> }, mariaRow };
});

vi.mock("../../../hooks/useTableURL", () => ({
  useTableURL: () => ({
    items: rowsHolder.rows,
    total: rowsHolder.rows.length,
    isLoading: false,
    params: { page: 1, pageSize: 20, q: "", sort: "name", order: "asc" },
    setParams: vi.fn(),
  }),
}));

import {
  downloadDatabaseBackup,
  restoreDatabaseUploadAuto,
} from "../../../apiClient";

const mockedDownload = downloadDatabaseBackup as ReturnType<typeof vi.fn>;
const mockedRestore = restoreDatabaseUploadAuto as ReturnType<typeof vi.fn>;

/** Opens the row's overflow menu (first action renders as a button, the
 * rest collapse into the "more" dropdown). Generous timeouts — under CI
 * load the dropdown animation + imperative modal mount can outlast
 * testing-library's 1s default. */
const openRowMenu = async () => {
  const moreBtn = await screen.findByRole(
    "button",
    { name: /more/i },
    { timeout: 5000 },
  );
  fireEvent.click(moreBtn);
};

beforeEach(() => {
  vi.clearAllMocks();
  // Restore the default single-MariaDB-row table (a PG test swaps it).
  rowsHolder.rows = [mariaRow];
  // jsdom doesn't implement createObjectURL — stub it so the save-as
  // path runs.
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: vi.fn(() => "blob:mock"),
    revokeObjectURL: vi.fn(),
  });
});

describe("UserDatabaseList backup/restore (GH #1045)", () => {
  it("downloads a backup via the save-as dance with the server filename", async () => {
    mockedDownload.mockResolvedValue({
      blob: new Blob(["SQL"], { type: "application/sql" }),
      filename: "shop_db-20260815-010203.sql",
    });

    const { unmount } = render(
      <App>
        <UserDatabaseList />
      </App>,
    );

    await openRowMenu();
    fireEvent.click(await screen.findByText("Download backup"));

    await waitFor(() => {
      expect(mockedDownload).toHaveBeenCalledWith("db1");
    });
    // The save-as continuation (blob -> object URL -> anchor click) and the
    // success toast keep scheduling React work after the assertion; let it
    // settle and unmount BEFORE the test ends, or the commit lands after
    // environment teardown and fails the whole run (CI flake, PR #1130).
    await waitFor(() => {
      expect(URL.createObjectURL).toHaveBeenCalled();
    });
    await act(async () => {});
    unmount();
  });

  it("restores from an uploaded .sql after confirmation", async () => {
    mockedRestore.mockResolvedValue(undefined);

    const { unmount } = render(
      <App>
        <UserDatabaseList />
      </App>,
    );

    await openRowMenu();
    fireEvent.click(
      await screen.findByText("Restore from file", undefined, {
        timeout: 5000,
      }),
    );

    // The confirm dialog gates the file picker.
    const okBtn = await screen.findByRole(
      "button",
      { name: /choose \.sql file/i },
      { timeout: 5000 },
    );
    fireEvent.click(okBtn);

    // The hidden file input is the upload target; simulate picking a file.
    const input = document.querySelector(
      'input[type="file"]',
    ) as HTMLInputElement;
    expect(input).not.toBeNull();
    const file = new File(["CREATE TABLE t (id INT);"], "dump.sql", {
      type: "application/sql",
    });
    fireEvent.change(input, { target: { files: [file] } });

    // GH #1044: the call now carries a third arg — the upload-progress
    // callback that drives the restore progress modal.
    await waitFor(() => {
      expect(mockedRestore).toHaveBeenCalledWith(
        "db1",
        file,
        expect.any(Function),
      );
    });
    // The progress modal reaches its success state after the upload resolves.
    expect(
      await screen.findByText(/restored from/i, undefined, { timeout: 5000 }),
    ).toBeInTheDocument();
    // Same teardown-race guard as the download test: flush the restore
    // continuation (modal state + query invalidation), then unmount.
    await act(async () => {});
    unmount();
  });

  // GH #1045 PostgreSQL parity: Restore from file is now enabled for Postgres
  // rows, and its confirm warns that the whole database is replaced (the
  // pg reset-restore drops + rebuilds, unlike MariaDB's per-table overwrite).
  it("enables Restore for Postgres with a whole-database warning", async () => {
    rowsHolder.rows = [{ ...mariaRow, name: "pg_shop", engine: "postgres" }];

    const { unmount } = render(
      <App>
        <UserDatabaseList />
      </App>,
    );

    await openRowMenu();

    // Restore is present and NOT disabled for Postgres.
    const restoreItem = await screen.findByText("Restore from file", undefined, {
      timeout: 5000,
    });
    const restoreLi = restoreItem.closest("li");
    expect(restoreLi).not.toBeNull();
    expect(restoreLi?.className).not.toMatch(/-item-disabled\b/);

    // Clicking opens the confirm with the Postgres whole-database wording.
    fireEvent.click(restoreItem);
    expect(
      await screen.findByText(/REPLACES the entire database/i, undefined, {
        timeout: 5000,
      }),
    ).toBeTruthy();
    await act(async () => {});
    unmount();
  });
});
