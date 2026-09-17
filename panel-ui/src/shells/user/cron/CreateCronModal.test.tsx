// CreateCronModal.test.tsx — GH #1686 item 1. Opening a tenant cron in Edit
// mode must immediately populate name/command with the job's values, without a
// browser refresh. The bug: the `form` instance lives in this never-unmounting
// component with antd's default `preserve`, so once the Create form has mounted
// once (store holds name:""/command:""), the Form's `initialValues` no longer
// override those preserved empties on the next Edit open — the fields stayed
// blank until a full-app refresh, and a Save from that blank form silently
// wiped the job. The fix drives name/command from the open-effect. This test
// reproduces the create→close→edit sequence against the same instance, so it is
// RED on the initialValues-only version and GREEN once the effect populates.
import { App as AntApp } from "antd";
import { render, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  createCronJob: vi.fn().mockResolvedValue({}),
  updateCronJob: vi.fn().mockResolvedValue({}),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

import { CreateCronModal } from "./CreateCronModal";
import type { CronJob } from "../../../apiClient";

const job: CronJob = {
  id: "j1",
  user_id: "u1",
  name: "nightly-backup",
  command: "php /home/u/example.com/public_html/backup.php",
  schedule: "0 3 * * *",
  enabled: true,
  last_run_at: null,
  last_exit_code: null,
  last_error: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
} as CronJob;

const noop = () => {};

const renderModal = (open: boolean, initial: CronJob | null) =>
  render(
    <AntApp>
      <CreateCronModal open={open} onClose={noop} onSuccess={noop} initial={initial} />
    </AntApp>,
  );

describe("CreateCronModal — Edit populates immediately (GH #1686)", () => {
  it("loads the job's name and command when opening Edit after the Create form was already mounted once", async () => {
    // 1) Open in Create mode so the persistent form store is seeded with the
    //    empty create defaults (this is what later shadows initialValues).
    const { rerender } = renderModal(true, null);
    await waitFor(() =>
      expect(document.querySelector("input#name")).not.toBeNull(),
    );

    // 2) Close — destroyOnClose unmounts the Form, but preserve keeps the
    //    empty name/command in the form store.
    rerender(
      <AntApp>
        <CreateCronModal open={false} onClose={noop} onSuccess={noop} initial={null} />
      </AntApp>,
    );

    // 3) Open the SAME instance in Edit mode for a real job.
    rerender(
      <AntApp>
        <CreateCronModal open={true} onClose={noop} onSuccess={noop} initial={job} />
      </AntApp>,
    );

    await waitFor(() => {
      const name = document.querySelector("input#name") as HTMLInputElement | null;
      const command = document.querySelector("textarea#command") as HTMLTextAreaElement | null;
      expect(name).not.toBeNull();
      expect(command).not.toBeNull();
      expect(name!.value).toBe(job.name);
      expect(command!.value).toBe(job.command);
    });
  });
});
