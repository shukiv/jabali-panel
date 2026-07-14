// MigrateImapDrawer.test.tsx — GH #390/#374. Locks the wire contract
// against admin_migrations_imap.go (probe → POST /admin/migrations/imap/
// probe; start → POST /admin/migrations/imap) and runs the real AntD
// Drawer/Form/Table so a runtime React/AntD break surfaces here (tsc /
// npm build don't catch those). Only apiClient is mocked.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MigrateImapDrawer } from "./MigrateImapDrawer";

vi.mock("../../../apiClient", () => ({
  apiClient: { post: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as { post: ReturnType<typeof vi.fn> };

function renderDrawer() {
  const qc = new QueryClient();
  render(
    <QueryClientProvider client={qc}>
      <App>
        <MigrateImapDrawer open onClose={() => {}} />
      </App>
    </QueryClientProvider>,
  );
}

async function fillConnection() {
  fireEvent.change(await screen.findByPlaceholderText("imap.gmail.com"), {
    target: { value: "imap.gmail.com" },
  });
  fireEvent.change(screen.getByPlaceholderText("old-account@company.com"), {
    target: { value: "old@company.com" },
  });
  fireEvent.change(screen.getByPlaceholderText("app-password"), {
    target: { value: "abcd efgh ijkl mnop" },
  });
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("MigrateImapDrawer", () => {
  it("probes the remote account and renders the folder map", async () => {
    mocked.post.mockResolvedValue({
      data: {
        total: 2,
        folders: [
          { name: "INBOX", role: "inbox", selectable: true },
          { name: "[Gmail]/All Mail", role: "all", selectable: true },
        ],
      },
    });
    renderDrawer();
    await fillConnection();
    fireEvent.click(screen.getByRole("button", { name: "Preview folders" }));

    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/admin/migrations/imap/probe", {
        host: "imap.gmail.com",
        port: 0,
        starttls: false,
        user: "old@company.com",
        password: "abcd efgh ijkl mnop",
        allow_private: false,
      }),
    );
    // Folder table rendered; All-Mail is flagged as skipped (duplicate).
    await screen.findByText("INBOX");
    await screen.findByText("skipped (duplicate of other folders)");
  });

  it("starts the migration with the destination mailbox", async () => {
    mocked.post.mockResolvedValue({ data: { job_id: "JOB123", state: "restoring" } });
    renderDrawer();
    await fillConnection();
    fireEvent.change(screen.getByPlaceholderText("new-account@example.com"), {
      target: { value: "new@example.com" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start migration" }));

    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/admin/migrations/imap", {
        host: "imap.gmail.com",
        port: 0,
        starttls: false,
        user: "old@company.com",
        password: "abcd efgh ijkl mnop",
        allow_private: false,
        to: "new@example.com",
      }),
    );
  });

  it("requires host, user, password and destination before starting", async () => {
    renderDrawer();
    fireEvent.click(await screen.findByRole("button", { name: "Start migration" }));
    // Validation blocks the request.
    await waitFor(() => expect(mocked.post).not.toHaveBeenCalled());
  });
});
