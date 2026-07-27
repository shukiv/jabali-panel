// AdminMigrationDetailPage.driveimport.test.tsx — GH #633/#634.
//
// Regression guard: the live-SSH "Run import" flow MUST write the plan's
// `preserve.credentials` before dispatching the import when the operator ticks
// "Carry over source passwords". The bug (GH #633/#634): this checkbox lived
// ONLY in CreateMigrationDrawer (the cpmove/quick-create path), so the SSH
// detail-page flow — pull-source → import — always ran preserve=OFF regardless
// of intent, and migrated MySQL users + mailboxes silently got fresh passwords.
//
// Wire contract locked here (against admin_migrations.go):
//   checked   → PUT /admin/migrations/:id/plan { preserve:{credentials:true} }
//               BEFORE POST /admin/migrations/:id/import
//   unchecked → POST /import only, NO plan write (preserve stays OFF = secure)
//
// Only apiClient is mocked; the real AntD Modal/Form/Checkbox run so an
// AntD/React break surfaces here (tsc / build don't catch those).
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DriveCard } from "./AdminMigrationDetailPage";

vi.mock("../../../apiClient", () => ({
  apiClient: { put: vi.fn(), post: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  put: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

const JOB_ID = "01JOBSSHTEST0000000000000";

function renderDrive() {
  const qc = new QueryClient();
  render(
    <QueryClientProvider client={qc}>
      <App>
        {/* hestiacp = live-SSH (not whm_pkgacct offline) → import enabled */}
        <DriveCard jobId={JOB_ID} jobState="ready" jobSourceKind="hestiacp" />
      </App>
    </QueryClientProvider>,
  );
}

async function openImportAndSetUser() {
  fireEvent.click(await screen.findByText("3. Run import…"));
  fireEvent.change(await screen.findByPlaceholderText("e.g. acme"), {
    target: { value: "notary" },
  });
}

describe("DriveCard SSH import — preserve passwords (GH #633/#634)", () => {
  beforeEach(() => {
    mocked.put.mockReset().mockResolvedValue({ data: { ok: true } });
    mocked.post.mockReset().mockResolvedValue({ data: { unit: "unit.service" } });
  });

  it("writes preserve.credentials plan BEFORE import when the box is ticked", async () => {
    renderDrive();
    await openImportAndSetUser();
    fireEvent.click(screen.getByRole("checkbox"));
    // Modal OK button label is "Run import"
    fireEvent.click(screen.getByRole("button", { name: "Run import" }));

    await waitFor(() => expect(mocked.post).toHaveBeenCalled());

    // plan written to the right endpoint with preserve.credentials
    expect(mocked.put).toHaveBeenCalledWith(
      `/admin/migrations/${JOB_ID}/plan`,
      expect.objectContaining({
        preserve: { credentials: true },
        areas: expect.objectContaining({ databases: true, mailboxes: true }),
      }),
    );
    // and import dispatched
    expect(mocked.post).toHaveBeenCalledWith(
      `/admin/migrations/${JOB_ID}/import`,
      expect.objectContaining({ target_user: "notary" }),
    );
    // ordering: plan write precedes import dispatch
    expect(mocked.put.mock.invocationCallOrder[0]).toBeLessThan(
      mocked.post.mock.invocationCallOrder[0],
    );
  });

  it("does NOT write a plan when the box is left unticked (preserve stays OFF)", async () => {
    renderDrive();
    await openImportAndSetUser();
    fireEvent.click(screen.getByRole("button", { name: "Run import" }));

    await waitFor(() => expect(mocked.post).toHaveBeenCalled());
    expect(mocked.put).not.toHaveBeenCalled();
    expect(mocked.post).toHaveBeenCalledWith(
      `/admin/migrations/${JOB_ID}/import`,
      expect.objectContaining({ target_user: "notary" }),
    );
  });
});
