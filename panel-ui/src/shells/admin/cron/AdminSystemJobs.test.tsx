// AdminSystemJobs.test.tsx — GH #1686 System jobs. Each row offers only the
// actions its job allows: Run now where a manual run is allowed (and asks
// first), a link to the page that owns the job's settings (Updates, Backups)
// instead of a second editor, and View log for the jobs that have one.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const navigate = vi.hoisted(() => vi.fn());
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => navigate };
});

vi.mock("../../../apiClient", () => ({
  listAdminSystemJobs: vi.fn(),
  runAdminSystemJob: vi.fn(),
  getAdminSystemJobLog: vi.fn().mockResolvedValue({ log: "", lines: 200 }),
  getCronJobLog: vi.fn(),
}));

// Confirm dialogs accept at once so the test drives the click-through.
vi.mock("../../../lib/feedback", () => ({
  feedback: {
    message: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
    modal: { confirm: vi.fn((opts: { onOk?: () => void }) => opts.onOk?.()) },
  },
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

import { AdminSystemJobs } from "./AdminSystemJobs";
import { listAdminSystemJobs, runAdminSystemJob, type AdminSystemJob } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

const job = (over: Partial<AdminSystemJob>): AdminSystemJob => ({
  id: "x",
  kind: "timer",
  label: "x",
  description: "does x",
  category: "maintenance",
  schedule: "Daily at 03:40",
  schedule_format: "text",
  status: "scheduled",
  last_result: "success",
  last_run_at: "2026-10-01T03:45:27Z",
  next_run_at: "2026-10-02T03:45:00Z",
  can_run_now: true,
  has_log: true,
  ...over,
});

const rows: AdminSystemJob[] = [
  job({ id: "retention-sweep", label: "Log retention sweep" }),
  job({
    id: "panel-update",
    label: "Panel auto-update",
    category: "updates",
    status: "disabled",
    last_result: "never",
    last_run_at: null,
    next_run_at: null,
    can_run_now: false,
    managed_by: "updates",
  }),
  job({ id: "malware-scan", label: "Daily malware scan", category: "security", status: "running", last_result: "running" }),
  job({
    id: "backup-schedule-S1",
    kind: "backup_schedule",
    label: "System backup",
    category: "backups",
    schedule: "0 2 * * *",
    schedule_format: "cron",
    last_result: "",
    can_run_now: false,
    has_log: false,
    managed_by: "backups",
  }),
];

const renderJobs = () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <AdminSystemJobs />
      </MemoryRouter>
    </QueryClientProvider>,
  );
};

const rowOf = async (label: string) => (await screen.findByText(label)).closest("tr") as HTMLElement;

describe("AdminSystemJobs (GH #1686)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(listAdminSystemJobs).mockResolvedValue({ data: rows, total: rows.length });
  });

  it("shows each job's status and only the actions it allows", async () => {
    renderJobs();

    const sweep = await rowOf("Log retention sweep");
    expect(within(sweep).getByText("Scheduled")).toBeTruthy();
    expect(within(sweep).getByRole("button", { name: /Run now/ })).toBeTruthy();

    const update = await rowOf("Panel auto-update");
    expect(within(update).getByText("Disabled")).toBeTruthy();
    expect(within(update).queryByRole("button", { name: /Run now/ })).toBeNull();
    fireEvent.click(within(update).getByRole("button", { name: /Open Updates/ }));
    expect(navigate).toHaveBeenCalledWith("/jabali-admin/updates");

    const scan = await rowOf("Daily malware scan");
    expect(within(scan).getByText("Running")).toBeTruthy();
    expect(within(scan).getByRole("button", { name: /Run now/ }).hasAttribute("disabled")).toBe(true);

    const backup = await rowOf("System backup");
    expect(within(backup).queryByRole("button", { name: /Run now/ })).toBeNull();
    expect(within(backup).getByRole("button", { name: /Open Backups/ })).toBeTruthy();
    expect(within(backup).queryByRole("button", { name: "More actions" })).toBeNull();
  });

  it("shows a disabled job's schedule as not scheduled", async () => {
    renderJobs();

    const update = await rowOf("Panel auto-update");
    expect(within(update).getByText("Not scheduled")).toBeTruthy();
    expect(within(update).getByText("Daily at 03:40 when enabled")).toBeTruthy();

    const sweep = await rowOf("Log retention sweep");
    expect(within(sweep).getByText("Daily at 03:40")).toBeTruthy();
    expect(within(sweep).queryByText("Not scheduled")).toBeNull();
  });

  it("asks before Run now, then starts the job", async () => {
    vi.mocked(runAdminSystemJob).mockResolvedValue(undefined);
    renderJobs();

    fireEvent.click(within(await rowOf("Log retention sweep")).getByRole("button", { name: /Run now/ }));

    expect(feedback.modal.confirm).toHaveBeenCalledWith(
      expect.objectContaining({ title: 'Run "Log retention sweep" now?', okText: "Run now" }),
    );
    await waitFor(() => expect(runAdminSystemJob).toHaveBeenCalledWith("retention-sweep"));
    await waitFor(() => expect(feedback.message.success).toHaveBeenCalledWith("Log retention sweep started"));
  });

  it("says so when the job is already running", async () => {
    vi.mocked(runAdminSystemJob).mockRejectedValue({ response: { status: 409, data: { error: "already_running" } } });
    renderJobs();

    fireEvent.click(within(await rowOf("Log retention sweep")).getByRole("button", { name: /Run now/ }));

    await waitFor(() => expect(feedback.message.info).toHaveBeenCalledWith("Log retention sweep is already running"));
    expect(feedback.message.error).not.toHaveBeenCalled();
  });
});
