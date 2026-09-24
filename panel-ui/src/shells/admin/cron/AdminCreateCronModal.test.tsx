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
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

import { AdminCreateCronModal } from "./AdminCreateCronModal";
import { createCronJob } from "../../../apiClient";

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
