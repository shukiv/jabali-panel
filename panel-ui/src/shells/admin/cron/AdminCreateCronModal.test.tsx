// AdminCreateCronModal.test.tsx — GH #1686 items 3+4. The admin Create Cron
// drawer must make the execution target clear: its top help paragraph has to
// change when the target switches Tenant -> root (item 4), and the command
// restrictions must be shown near the command field (item 3). Before this slice
// the paragraph was static ("...runs as that tenant's Linux user inside their
// cgroup slice") no matter the target, and there was no command-restriction copy
// at all — so an admin creating a root cron had no way to know why `ls` was
// rejected. This test drives the radio and asserts the paragraph swaps.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn().mockResolvedValue({ data: { data: [] } }) },
  createCronJob: vi.fn().mockResolvedValue({}),
  updateCronJob: vi.fn().mockResolvedValue({}),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

import { AdminCreateCronModal } from "./AdminCreateCronModal";
import { createCronJob, updateCronJob } from "../../../apiClient";
import type { CronWorkspaceRow } from "../../../components/cron/cronColumns";

const noop = () => {};

const renderModal = () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <AntApp>
        <AdminCreateCronModal open onClose={noop} onSuccess={noop} />
      </AntApp>
    </QueryClientProvider>,
  );
};

const tenantJob: CronWorkspaceRow = {
  id: "j1",
  user_id: "u1",
  username: "alice",
  name: "nightly",
  command: "php /home/alice/example.com/public_html/cron.php",
  schedule: "0 3 * * *",
  enabled: true,
  run_as_root: false,
  last_run_at: null,
  last_exit_code: null,
  last_error: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

// Edit-mode harness: one QueryClient across rerenders so the drawer can be
// opened, closed and re-opened with a different `initial` (the persistent-form
// trap from GH #1686 item 1 only shows across opens).
const renderEditor = (initial: CronWorkspaceRow | null, open = true) => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const ui = (o: boolean, i: CronWorkspaceRow | null) => (
    <QueryClientProvider client={qc}>
      <AntApp>
        <AdminCreateCronModal open={o} onClose={noop} onSuccess={noop} initial={i} />
      </AntApp>
    </QueryClientProvider>
  );
  const r = render(ui(open, initial));
  return { ...r, reopen: (o: boolean, i: CronWorkspaceRow | null) => r.rerender(ui(o, i)) };
};

const inputValue = (root: HTMLElement, sel: string) =>
  (root.querySelector(sel) as HTMLInputElement | HTMLTextAreaElement | null)?.value;

describe("AdminCreateCronModal — target-aware help (GH #1686 items 3+4)", () => {
  it("swaps the help paragraph from tenant to root when the target changes", async () => {
    // antd Drawer renders into a portal, so assert against baseElement (body).
    const { baseElement } = renderModal();

    // Default target is Tenant: tenant help present, root help absent.
    expect(baseElement.textContent).toContain("inside their cgroup slice");
    expect(baseElement.textContent).not.toContain("system-scoped systemd timer");

    // Switch the target to root.
    fireEvent.click(baseElement.querySelector('input[type="radio"][value="root"]')!);

    await waitFor(() => {
      expect(baseElement.textContent).toContain("system-scoped systemd timer");
    });
    expect(baseElement.textContent).not.toContain("inside their cgroup slice");
  });

  it("shows the command restrictions near the command field", () => {
    const { baseElement } = renderModal();
    // The shared CronCommandHelp names the reporter's rejected examples so the
    // admin can see why a plain command fails.
    expect(baseElement.textContent).toContain("Commands must start with");
    expect(baseElement.textContent).toContain("will not work");
  });
});

describe("AdminCreateCronModal — friendly validation errors (GH #1686 item 5)", () => {
  it("renders the shared headline for a structured cronops error, not the raw detail", async () => {
    // Backend rejects with the structured validation_failed shape the fixed API
    // now sends (code + clean detail). Before this slice the admin door showed
    // data.detail verbatim; now it must render the shared friendly headline.
    vi.mocked(createCronJob).mockRejectedValueOnce({
      response: {
        data: {
          error: "validation_failed",
          field: "command",
          code: "binary_not_allowed",
          detail: 'first token must be "wp", got "ls"',
        },
      },
    });

    const { baseElement } = renderModal();

    // Root target so the (empty) tenant picker's required user_id doesn't block submit.
    fireEvent.click(baseElement.querySelector('input[type="radio"][value="root"]')!);
    fireEvent.change(baseElement.querySelector("#name")!, { target: { value: "nightly" } });
    fireEvent.change(baseElement.querySelector("#command")!, { target: { value: "ls -la" } });
    fireEvent.click(baseElement.querySelector("button.ant-btn-primary")!);

    await waitFor(() => {
      expect(baseElement.textContent).toContain("Command must start with wp, php, python, or node");
    });
    // The raw backend detail must never reach the user.
    expect(baseElement.textContent).not.toContain('first token must be "wp"');
  });
});

describe("AdminCreateCronModal — edit a tenant's job (GH #1686 item 2)", () => {
  it("populates the job and shows the owner read-only instead of the target picker", async () => {
    const { baseElement } = renderEditor(tenantJob);

    await waitFor(() => expect(inputValue(baseElement, "#name")).toBe("nightly"));
    expect(inputValue(baseElement, "#command")).toBe(tenantJob.command);
    expect(
      (baseElement.querySelector('input[type="radio"][value="0 3 * * *"]') as HTMLInputElement).checked,
    ).toBe(true);
    // Owner / run-as are immutable on edit: no target radio, no tenant picker.
    expect(baseElement.querySelector('input[type="radio"][value="root"]')).toBeNull();
    expect(baseElement.querySelector("#user_id")).toBeNull();
    expect(baseElement.textContent).toContain("alice");
    expect(baseElement.textContent).toContain("admincreatecronmodal.edit_cron_job_as_tenant");
    expect(baseElement.textContent).toContain("inside their cgroup slice");
  });

  it("opens a non-preset schedule in advanced mode with the raw expression", async () => {
    const { baseElement } = renderEditor({ ...tenantJob, schedule: "*/7 * * * *" });
    await waitFor(() => expect(inputValue(baseElement, "#schedule")).toBe("*/7 * * * *"));
  });

  it("saves through updateCronJob with only the editable fields", async () => {
    vi.mocked(createCronJob).mockClear();
    vi.mocked(updateCronJob).mockClear();
    const { baseElement } = renderEditor(tenantJob);
    await waitFor(() => expect(inputValue(baseElement, "#name")).toBe("nightly"));

    fireEvent.change(baseElement.querySelector("#name")!, { target: { value: "renamed" } });
    fireEvent.click(baseElement.querySelector("button.ant-btn-primary")!);

    await waitFor(() => expect(updateCronJob).toHaveBeenCalledTimes(1));
    // Exactly the editable fields — never user_id / run_as_root.
    expect(vi.mocked(updateCronJob).mock.calls[0]).toEqual([
      "j1",
      { name: "renamed", command: tenantJob.command, schedule: "0 3 * * *" },
    ]);
    expect(createCronJob).not.toHaveBeenCalled();
  });

  it("describes a root job as root, not as a tenant job", async () => {
    const { baseElement } = renderEditor({ ...tenantJob, username: "admin", run_as_root: true });
    await waitFor(() => expect(inputValue(baseElement, "#name")).toBe("nightly"));
    expect(baseElement.textContent).toContain("admincreatecronmodal.edit_cron_job_as_root");
    expect(baseElement.textContent).toContain("system-scoped systemd timer");
    expect(baseElement.textContent).not.toContain("inside their cgroup slice");
  });

  it("re-populates on every open — a Create open never blanks the next Edit", async () => {
    // Open in Create mode, type a name, close, then open Edit for a job: the
    // persistent form store must not shadow the job's values.
    const { baseElement, reopen } = renderEditor(null);
    fireEvent.change(baseElement.querySelector("#name")!, { target: { value: "draft" } });
    reopen(false, null);
    reopen(true, tenantJob);
    await waitFor(() => expect(inputValue(baseElement, "#name")).toBe("nightly"));
    expect(inputValue(baseElement, "#command")).toBe(tenantJob.command);

    // And back to Create: the job's values must not leak into a new job.
    reopen(false, null);
    reopen(true, null);
    await waitFor(() => expect(inputValue(baseElement, "#name")).toBe(""));
  });
});
