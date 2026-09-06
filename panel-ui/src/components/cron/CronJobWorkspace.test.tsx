// CronJobWorkspace.test.tsx — JAB-298. AC6: the admin and tenant workspaces
// must cover the SAME common-action matrix. The matrix is written once and
// invoked for both real adapters, so the two screens cannot drift on the
// shared behavior (toggle / run / delete / log / busy / backend-detail errors /
// overlays), while each keeps its own audience policy (owner-aware search,
// pagination, editor). The editors are mocked to a probe div (they stay
// distinct per the ticket constraint); feedback.modal.confirm is short-circuited
// to its onOk so the delete path is testable without the imperative modal.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  listCronJobs: vi.fn(),
  listAdminCronJobs: vi.fn(),
  deleteCronJob: vi.fn(),
  updateCronJob: vi.fn(),
  runCronJobNow: vi.fn(),
  getCronJobLog: vi.fn(),
}));
vi.mock("../../apiClient", () => api);

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

// RowActions' delete confirm and CronLogDrawer's copy toast go through feedback;
// short-circuit confirm to onOk so the delete action reaches deleteCronJob.
vi.mock("../../lib/feedback", () => ({
  feedback: {
    modal: { confirm: (o: { onOk?: () => void }) => o.onOk?.() },
    message: { success: vi.fn(), error: vi.fn() },
  },
}));

// Editors stay distinct (ticket constraint); mock each to a probe exposing the
// open flag and the initial job id so New vs Edit wiring is assertable.
vi.mock("../../shells/admin/cron/AdminCreateCronModal", () => ({
  AdminCreateCronModal: ({ open }: { open: boolean }) => (
    <div data-testid="editor" data-open={String(open)} data-initial="" />
  ),
}));
vi.mock("../../shells/user/cron/CreateCronModal", () => ({
  CreateCronModal: ({ open, initial }: { open: boolean; initial?: { id: string } | null }) => (
    <div data-testid="editor" data-open={String(open)} data-initial={initial?.id ?? ""} />
  ),
}));

import { AdminCronList } from "../../shells/admin/cron/AdminCronList";
import { UserCronList } from "../../shells/user/cron/UserCronList";

type Row = {
  id: string;
  user_id: string;
  name: string;
  command: string;
  schedule: string;
  enabled: boolean;
  last_run_at: string | null;
  last_exit_code: number | null;
  last_error: string | null;
  created_at: string;
  updated_at: string;
  username?: string;
};

const makeRow = (over: Partial<Row>): Row => ({
  id: "j1",
  user_id: "u1",
  name: "alpha",
  command: "echo hi",
  schedule: "0 3 * * *",
  enabled: true,
  last_run_at: null,
  last_exit_code: null,
  last_error: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  ...over,
});

const renderAdapter = (Adapter: () => JSX.Element) => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <AntApp>
          <Adapter />
        </AntApp>
      </MemoryRouter>
    </QueryClientProvider>,
  );
};

interface MatrixOpts {
  Adapter: () => JSX.Element;
  listMock: typeof api.listCronJobs;
  runLabel: string;
  searchOwner: boolean; // admin includes the owner username in search
  paginated: boolean; // admin paginates; tenant does not
  canEdit: boolean; // tenant offers an Edit action
}

const setList = (mock: typeof api.listCronJobs, rows: Row[]) =>
  mock.mockResolvedValue({ items: rows });

const commonActionMatrix = (label: string, opts: MatrixOpts) => {
  describe(`${label} cron workspace (common action matrix)`, () => {
    beforeEach(() => {
      Object.values(api).forEach((m) => m.mockReset());
      api.updateCronJob.mockResolvedValue({});
      api.deleteCronJob.mockResolvedValue({});
      api.getCronJobLog.mockResolvedValue({ log: "some log", lines: 1 });
      api.runCronJobNow.mockResolvedValue({ exit_code: 0, stdout: "ok", stderr: "" });
    });

    it("renders the list", async () => {
      setList(opts.listMock, [makeRow({ id: "j1", name: "alpha", username: "bob" })]);
      renderAdapter(opts.Adapter);
      expect(await screen.findByText("alpha")).toBeInTheDocument();
    });

    it("filters by owner username only when the audience is owner-aware", async () => {
      setList(opts.listMock, [
        makeRow({ id: "j1", name: "alpha", username: "zephyr" }),
        makeRow({ id: "j2", name: "beta", username: "quill" }),
      ]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.change(screen.getByRole("searchbox"), { target: { value: "zephyr" } });
      if (opts.searchOwner) {
        expect(screen.getByText("alpha")).toBeInTheDocument();
        expect(screen.queryByText("beta")).not.toBeInTheDocument();
      } else {
        // username is not in the tenant haystack — nothing matches "zephyr".
        expect(screen.queryByText("alpha")).not.toBeInTheDocument();
        expect(screen.queryByText("beta")).not.toBeInTheDocument();
      }
    });

    it("toggles enabled through the shared handler", async () => {
      setList(opts.listMock, [makeRow({ id: "j1", enabled: true })]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.click(screen.getByRole("switch"));
      await waitFor(() => expect(api.updateCronJob).toHaveBeenCalledWith("j1", { enabled: false }));
    });

    it("runs a job and shows the run-result overlay", async () => {
      setList(opts.listMock, [makeRow({ id: "j1" })]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.click(screen.getByText(opts.runLabel));
      await waitFor(() => expect(api.runCronJobNow).toHaveBeenCalledWith("j1"));
      expect(await screen.findByText(/Exit code: 0/)).toBeInTheDocument();
    });

    it("surfaces the backend detail on a failed action", async () => {
      setList(opts.listMock, [makeRow({ id: "j1", enabled: true })]);
      api.updateCronJob.mockRejectedValue({ response: { data: { detail: "boom from backend" } } });
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.click(screen.getByRole("switch"));
      expect(await screen.findByText("boom from backend")).toBeInTheDocument();
    });

    it("opens the log drawer from the overflow menu", async () => {
      setList(opts.listMock, [makeRow({ id: "j1" })]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.click(screen.getByLabelText("More actions"));
      fireEvent.click(await screen.findByText("Log"));
      expect(await screen.findByText("cronlogdrawer.cron_job_log")).toBeInTheDocument();
    });

    it("deletes a job through the shared handler", async () => {
      setList(opts.listMock, [makeRow({ id: "j1" })]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.click(screen.getByLabelText("More actions"));
      fireEvent.click(await screen.findByText("Delete"));
      await waitFor(() => expect(api.deleteCronJob).toHaveBeenCalledWith("j1"));
    });

    it(`${opts.paginated ? "paginates" : "does not paginate"} per the audience`, async () => {
      const rows = Array.from({ length: 30 }, (_, i) =>
        makeRow({ id: `j${i}`, name: `job-${i}`, username: `owner-${i}` }),
      );
      setList(opts.listMock, rows);
      const { container } = renderAdapter(opts.Adapter);
      await screen.findByText("job-0");
      if (opts.paginated) {
        expect(container.querySelector(".ant-pagination")).not.toBeNull();
      } else {
        expect(container.querySelector(".ant-pagination")).toBeNull();
      }
    });

    it("opens the editor for a new job with no initial", async () => {
      setList(opts.listMock, [makeRow({ id: "j1" })]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      expect(screen.getByTestId("editor").getAttribute("data-open")).toBe("false");
      fireEvent.click(screen.getByText("New Cron Job"));
      const editor = screen.getByTestId("editor");
      expect(editor.getAttribute("data-open")).toBe("true");
      expect(editor.getAttribute("data-initial")).toBe("");
    });

    it(`${opts.canEdit ? "offers" : "omits"} the Edit action`, async () => {
      setList(opts.listMock, [makeRow({ id: "j1" })]);
      renderAdapter(opts.Adapter);
      await screen.findByText("alpha");
      fireEvent.click(screen.getByLabelText("More actions"));
      if (opts.canEdit) {
        fireEvent.click(await screen.findByText("Edit"));
        const editor = screen.getByTestId("editor");
        expect(editor.getAttribute("data-open")).toBe("true");
        expect(editor.getAttribute("data-initial")).toBe("j1");
      } else {
        await screen.findByText("Delete"); // menu is open
        expect(screen.queryByText("Edit")).not.toBeInTheDocument();
      }
    });
  });
};

commonActionMatrix("admin", {
  Adapter: AdminCronList,
  listMock: api.listAdminCronJobs,
  runLabel: "Run",
  searchOwner: true,
  paginated: true,
  canEdit: false,
});

commonActionMatrix("tenant", {
  Adapter: UserCronList,
  listMock: api.listCronJobs,
  runLabel: "Run now",
  searchOwner: false,
  paginated: false,
  canEdit: true,
});
