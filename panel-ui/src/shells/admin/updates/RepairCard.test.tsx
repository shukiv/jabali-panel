// RepairCard.test.tsx — GH #2266. Auto-Repair runs `jabali repair --auto`,
// which leaves the destructive steps and the ones with no fix. A broken row it
// leaves says so and names the command, and the button is off when it has
// nothing to fix. Only apiClient is mocked; TanStack + AntD run for real.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { App } from "antd";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";
import { RepairCard } from "./RepairCard";

const mockPost = apiClient.post as ReturnType<typeof vi.fn>;

// Lines as `jabali repair --diagnose` prints them (repair.go runRepair).
const DOCROOT_BROKEN =
  "  ✗ [docroot-www-data-group] web docroot files not group www-data / dirs not setgid (nginx 403 on newly uploaded media)\n" +
  "     BROKEN — 2/3 docroot(s) with files unreadable by nginx: a.example, b.example\n";
const GIT_POINTER_BROKEN =
  "  ✗ [git-pointer] /opt/jabali-panel/.git points at a missing worktree [destructive]\n" +
  "     BROKEN — gitdir missing\n";
const FPM_BROKEN =
  "  ✗ [fpm-masters-down] a per-user PHP-FPM master is crash-looping (sites 500/502 with missing fpm.sock) [manual]\n" +
  "     BROKEN — jabali-fpm@alice: bad extension\n";
const OK_LINE =
  "  ✓ [git-ownership] repo ownership\n" +
  "     OK\n";

function diagnoseReturns(output: string) {
  mockPost.mockImplementation((url: string) =>
    url === "/admin/updates/repair/diagnose"
      ? Promise.resolve({ data: { output: "Diagnostics:\n" + output, exit_code: 0 } })
      : Promise.resolve({ data: { unit: "jabali-repair-oneshot.service", started_at: "now" } }),
  );
}

async function runDiagnostics() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <RepairCard />
      </App>
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: /Run Diagnostics/ }));
  await screen.findByText(/issues? detected/);
}

const autoRepairButton = () => screen.getByRole("button", { name: /Auto-Repair safe issues/ });

beforeEach(() => {
  vi.clearAllMocks();
});

describe("RepairCard", () => {
  it("names the console command for a destructive row Auto-Repair leaves", async () => {
    diagnoseReturns(OK_LINE + GIT_POINTER_BROKEN);
    await runDiagnostics();

    expect(screen.getByText("/opt/jabali-panel/.git points at a missing worktree")).toBeInTheDocument();
    expect(screen.queryByText(/\[destructive\]/)).not.toBeInTheDocument();
    expect(screen.getByText("jabali repair --git-pointer --yes")).toBeInTheDocument();
    expect(screen.getByText(/Auto-Repair can't fix these/)).toBeInTheDocument();
    expect(autoRepairButton()).toBeDisabled();
  });

  it("says a row with no fix needs a decision", async () => {
    diagnoseReturns(FPM_BROKEN);
    await runDiagnostics();

    expect(
      screen.getByText("a per-user PHP-FPM master is crash-looping (sites 500/502 with missing fpm.sock)"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/\[manual\]/)).not.toBeInTheDocument();
    expect(screen.getByText(/it needs your decision/)).toBeInTheDocument();
    expect(autoRepairButton()).toBeDisabled();
  });

  it("leaves a row Auto-Repair fixes unmarked and counts it", async () => {
    diagnoseReturns(DOCROOT_BROKEN + GIT_POINTER_BROKEN + FPM_BROKEN);
    await runDiagnostics();

    expect(screen.getByText("3 issues detected")).toBeInTheDocument();
    expect(screen.getByText(/Auto-Repair fixes 1 of them/)).toBeInTheDocument();
    expect(screen.getAllByText(/Auto-Repair leaves this one/)).toHaveLength(2);
    expect(autoRepairButton()).toBeEnabled();
  });

  it("offers Auto-Repair for safe rows only", async () => {
    diagnoseReturns(OK_LINE + DOCROOT_BROKEN);
    await runDiagnostics();

    expect(screen.getByText("1 issue detected")).toBeInTheDocument();
    expect(screen.queryByText(/Auto-Repair leaves this one/)).not.toBeInTheDocument();
    expect(autoRepairButton()).toBeEnabled();
  });
});
