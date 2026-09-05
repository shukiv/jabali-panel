// AddGrantModal.test.tsx — GH #1415.
//
// On PostgreSQL the agent runs GRANT ALL regardless of level (ADR-0091), so the
// "Grant Type" / preset / custom-privilege controls are inert — a read-only or
// per-privilege choice is silently upgraded to full access. The modal must hide
// those controls for a postgres user and POST a fixed grant_level "rw", while
// keeping the full choice for mariadb.
import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AddGrantModal } from "./AddGrantModal";

vi.mock("../apiClient", () => ({ apiClient: { post: vi.fn() } }));
vi.mock("../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));
vi.mock("../hooks/useQueries", () => ({ useListQuery: vi.fn() }));

import { apiClient } from "../apiClient";
import { useListQuery } from "../hooks/useQueries";

const mockedPost = apiClient.post as unknown as ReturnType<typeof vi.fn>;
const mockedList = useListQuery as unknown as ReturnType<typeof vi.fn>;

type Db = { id: string; name: string; engine: "mariadb" | "postgres" };

function renderModal(userEngine: "mariadb" | "postgres", items: Db[]) {
  mockedList.mockReturnValue({ items, isLoading: false });
  render(
    <App>
      <AddGrantModal
        open
        userId="du1"
        username="app_user"
        excludedDatabaseIds={[]}
        userEngine={userEngine}
        onClose={() => {}}
        onSuccess={() => {}}
      />
    </App>,
  );
}

describe("AddGrantModal PostgreSQL gate (GH #1415)", () => {
  beforeEach(() => {
    mockedPost.mockReset().mockResolvedValue({ data: {} });
    mockedList.mockReset();
  });

  it("hides the inert grant controls and POSTs a fixed rw level for postgres", async () => {
    renderModal("postgres", [{ id: "pg1", name: "pgdb", engine: "postgres" }]);

    // The level/privilege controls must be gone — they aren't honoured on PG.
    expect(screen.queryByText("Grant Type")).toBeNull();
    expect(screen.queryByText("Custom Privileges")).toBeNull();
    expect(screen.queryByText("Read Only (SELECT only)")).toBeNull();
    // ...and the note explains why.
    expect(screen.getByText(/full access to the selected database/i)).toBeTruthy();

    fireEvent.mouseDown(screen.getByRole("combobox"));
    fireEvent.click(screen.getByText("pgdb"));
    fireEvent.click(screen.getByRole("button", { name: "Grant Access" }));

    await waitFor(() => expect(mockedPost).toHaveBeenCalled());
    const [url, body] = mockedPost.mock.calls[0];
    expect(url).toBe("/database-users/du1/grants");
    expect(body).toEqual({ database_id: "pg1", grant_level: "rw" });
  });

  it("keeps the grant-type controls for mariadb", async () => {
    renderModal("mariadb", [{ id: "m1", name: "mdb", engine: "mariadb" }]);

    expect(screen.getByText("Grant Type")).toBeTruthy();
    expect(screen.getByText("Preset Privileges")).toBeTruthy();
    expect(screen.queryByText(/full access to the selected database/i)).toBeNull();

    fireEvent.mouseDown(screen.getByRole("combobox"));
    fireEvent.click(screen.getByText("mdb"));
    fireEvent.click(screen.getByRole("button", { name: "Grant Access" }));

    await waitFor(() => expect(mockedPost).toHaveBeenCalled());
    const [, body] = mockedPost.mock.calls[0];
    expect(body).toEqual({ database_id: "m1", grant_level: "rw" });
  });
});
