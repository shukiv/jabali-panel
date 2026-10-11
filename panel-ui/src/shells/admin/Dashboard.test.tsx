// Dashboard.test.tsx — the admin Dashboard's community-mix layout (GH #1249,
// Option 7): host banner, count cards (now with Applications), the tabbed
// surface and the services rail.
//
// The pins that matter for regressions:
//   - landing fetches only the Activity rows; every other tab's list loads on
//     first click and its pane unmounts when another tab opens (#981 — an idle
//     Dashboard must not fan out list requests);
//   - the Alerts badge counts the host alerts and ?tab=alerts opens them;
//   - the rail's "N of M running" counts units meant to run, so an idle-by-
//     design unit (jabali-webmail before mail is on) is not a missing one.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ServerStatusEnvelope } from "../../hooks/useServerStatus";

const auditRow = {
  id: "a1",
  ts: "2026-10-11T10:00:00Z",
  actor_kind: "user",
  actor_name: "shuki",
  action: "POST /api/v1/domains",
  target_type: "domain",
  target_id: "01DOMAIN",
  result: "ok",
};

const mockGet = vi.fn(async (url: string) => {
  if (url === "/admin/counts") {
    return { data: { users: 128, domains: 342, mailboxes: 1046, applications: 87 } };
  }
  if (url.startsWith("/admin/audit")) return { data: { data: [auditRow], total: 1 } };
  if (url.startsWith("/admin/backup-runs")) {
    return {
      data: {
        data: [
          {
            run_id: "run1",
            kind: "account_backup",
            has_accounts: true,
            accounts: 42,
            total: 42,
            succeeded: 41,
            failed: 1,
            running: 0,
            queued: 0,
            cancelled: 0,
            partial: 0,
            started_at: "2026-10-11T04:00:00Z",
          },
        ],
        manual: [],
      },
    };
  }
  if (url.startsWith("/databases")) {
    return { data: { data: [{ id: "db1", name: "shop_wp", engine: "mariadb" }], total: 1 } };
  }
  return { data: { data: [], total: 0 } };
});
vi.mock("../../apiClient", () => ({
  apiClient: { get: (url: string) => mockGet(url) },
}));

let env: ServerStatusEnvelope;
vi.mock("../../hooks/useServerStatus", () => ({
  useServerStatus: () => ({ data: env }),
}));

let caps: Record<string, unknown>;
vi.mock("../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: caps }),
}));

vi.mock("../../hooks/useSystemUpdates", () => ({
  useUpdateState: () => ({ data: { jabali_behind: 0, jabali_current_sha: "44b690612abcdef" } }),
}));
vi.mock("./updates/UpdatesPendingBanner", () => ({
  UpdatesPendingBanner: () => null,
  OsSecurityUpdatesBanner: () => null,
}));

import { Dashboard } from "./Dashboard";

const svc = (unit: string, active: string, unit_file_state: string) => ({
  unit,
  active,
  sub: active === "active" ? "running" : "dead",
  load_state: "loaded",
  unit_file_state,
  memory_bytes: 0,
  tasks: 0,
  uptime_seconds: 0,
});

function makeEnv(): ServerStatusEnvelope {
  return {
    as_of: "2026-10-11T10:00:00Z",
    host: {
      hostname: "panel.acme-host.com",
      os: "Debian GNU/Linux 13",
      kernel: "6.12",
      cpu_model: "x",
      timezone: "UTC",
      uptime_seconds: 41 * 86400 + 3600,
      load_avg: [0, 0, 0],
      cpu_count: 4,
      mem_total_kb: 1000,
      mem_available_kb: 390,
      mem_used_kb: 610,
      swap_total_kb: 0,
      swap_used_kb: 0,
      // df semantics (GH #2029): 780 / (780 + 220) = 78%, though the
      // partition's size counts a reserve on top.
      partitions: [{ mount_point: "/", total_bytes: 1100, used_bytes: 780, free_bytes: 220 }],
      ntp_synced: true,
    },
    cpu: { usage_percent: 23.4, iowait_percent: 0, per_core: [], warming_up: false, as_of: "" },
    network: null,
    processes: null,
    services: {
      services: [
        svc("nginx.service", "active", "enabled"),
        svc("mariadb.service", "active", "enabled"),
        svc("crowdsec.service", "failed", "enabled"),
        // Idle by design: not run-configured, so neither running nor missing.
        svc("jabali-webmail.service", "inactive", "disabled"),
        // Hidden: Postgres is not enabled as a feature here.
        svc("postgresql.service", "inactive", "disabled"),
      ],
    },
    user_slices: null,
    software: null,
    queues: null,
    alerts: [
      { level: "warning", kind: "disk", detail: "Disk / is 81% full" },
      { level: "critical", kind: "service", detail: "crowdsec.service has failed" },
    ],
  };
}

function renderDash(path = "/jabali-admin/dashboard") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Dashboard />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const requested = (prefix: string) =>
  mockGet.mock.calls.some(([url]) => (url as string).startsWith(prefix));

describe("admin Dashboard (GH #1249 Option 7)", () => {
  beforeEach(() => {
    mockGet.mockClear();
    env = makeEnv();
    caps = { mail_enabled: true, postgres_enabled: false, public_ipv4: "192.0.2.10", public_ipv6: "" };
  });

  it("shows the four count cards, Applications included, from one /admin/counts call", async () => {
    renderDash();
    const apps = await screen.findByRole("link", { name: /Applications/ });
    expect(apps).toHaveAttribute("href", "/jabali-admin/applications");
    await waitFor(() => expect(apps).toHaveTextContent("87"));
    expect(screen.getByRole("link", { name: /Mailboxes/ })).toHaveTextContent("1,046");
    expect(mockGet.mock.calls.filter(([u]) => u === "/admin/counts")).toHaveLength(1);
  });

  it("leaves the Mailboxes card out when the mail module is off (GH #1417)", async () => {
    caps = { ...caps, mail_enabled: false };
    renderDash();
    await screen.findByRole("link", { name: /Applications/ });
    expect(screen.queryByRole("link", { name: /Mailboxes/ })).not.toBeInTheDocument();
  });

  it("shows the host, its build and uptime, and the three meters", async () => {
    renderDash();
    expect(screen.getByText("panel.acme-host.com")).toBeInTheDocument();
    expect(screen.getByText("Debian GNU/Linux 13 · Jabali 44b6906 · up 41 days")).toBeInTheDocument();
    expect(screen.getByText("23%")).toBeInTheDocument();
    expect(screen.getByText("61%")).toBeInTheDocument();
    expect(screen.getByText("78%")).toBeInTheDocument();
    expect(screen.getByText("1 critical")).toBeInTheDocument();
  });

  it("shows a dash, not 0%, while the CPU sample is warming up", () => {
    env.cpu = { ...env.cpu!, warming_up: true, usage_percent: 0 };
    renderDash();
    expect(screen.queryByText("0%")).not.toBeInTheDocument();
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
  });

  it("fetches only the Activity rows on landing", async () => {
    renderDash();
    expect(await screen.findByText("shuki")).toBeInTheDocument();
    expect(screen.getByText("Create domain")).toBeInTheDocument();
    for (const prefix of ["/users", "/domains", "/databases", "/packages", "/admin/backup-runs"]) {
      expect(requested(prefix)).toBe(false);
    }
  });

  it("loads a tab's list on first click and unmounts the pane it leaves", async () => {
    renderDash();
    await screen.findByText("shuki");

    fireEvent.click(screen.getByRole("tab", { name: /Backups/ }));
    expect(await screen.findByText("1 failed")).toBeInTheDocument();
    expect(screen.getByText("41 succeeded")).toBeInTheDocument();
    expect(requested("/admin/backup-runs")).toBe(true);
    expect(screen.queryByText("shuki")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: /Domains \/ Users \/ Databases/ }));
    expect(await screen.findByText("shop_wp")).toBeInTheDocument();
    expect(requested("/databases")).toBe(true);
    expect(requested("/packages")).toBe(false);
  });

  it("badges the Alerts tab with the alert count and lists critical first", async () => {
    renderDash();
    const tab = screen.getByRole("tab", { name: /Alerts/ });
    expect(within(tab).getByText("2")).toBeInTheDocument();

    fireEvent.click(tab);
    const details = await screen.findAllByText(/has failed|% full/);
    expect(details.map((d) => d.textContent)).toEqual([
      "crowdsec.service has failed",
      "Disk / is 81% full",
    ]);
  });

  it("opens the tab named in ?tab=, and Activity for an unknown one", async () => {
    const { unmount } = renderDash("/jabali-admin/dashboard?tab=alerts");
    expect(await screen.findByText("crowdsec.service has failed")).toBeInTheDocument();
    expect(requested("/admin/audit")).toBe(false);
    unmount();

    renderDash("/jabali-admin/dashboard?tab=nonsense");
    expect(await screen.findByText("shuki")).toBeInTheDocument();
  });

  it("counts the rail's running units against the units meant to run", () => {
    renderDash();
    expect(screen.getByText("2 of 3 running")).toBeInTheDocument();
    expect(screen.getByText("webmail")).toBeInTheDocument();
    expect(screen.getByText("idle")).toBeInTheDocument();
    expect(screen.queryByText("postgresql")).not.toBeInTheDocument();
  });
});
