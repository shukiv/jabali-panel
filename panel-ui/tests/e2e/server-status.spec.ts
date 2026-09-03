// Server Status E2E — drives /jabali-admin/server-status against a
// mocked /admin/server-status envelope. Confirms the page composes its
// six sections (header, meters, disks, network, services, processes)
// from a single REST round-trip.
//
// We don't try to drive a real agent here — that's a host-level
// validation. The unit tests already cover the agent and aggregator;
// this spec catches UI regressions only.
import { admin, expect, mockApi, signIn, test } from "./fixtures";

const fakeEnvelope = {
  as_of: new Date().toISOString(),
  alerts: [
    { level: "critical", kind: "service", detail: "mariadb.service is inactive" },
  ],
  host: {
    hostname: "mx.jabali-panel.local",
    os: "Debian GNU/Linux 13 (trixie)",
    kernel: "6.12.74-amd64",
    cpu_model: "Intel Xeon",
    timezone: "UTC",
    uptime_seconds: 12345,
    load_avg: [0.5, 0.4, 0.3],
    cpu_count: 4,
    mem_total_kb: 4_000_000,
    mem_available_kb: 3_000_000,
    mem_used_kb: 1_000_000,
    swap_total_kb: 0,
    swap_used_kb: 0,
    partitions: [
      { mount_point: "/", total_bytes: 10_000_000_000, used_bytes: 4_000_000_000, free_bytes: 6_000_000_000 },
    ],
    ntp_synced: true,
  },
  cpu: {
    usage_percent: 12.5,
    iowait_percent: 0.5,
    per_core: [10, 15, 12, 13],
    warming_up: false,
    as_of: new Date().toISOString(),
  },
  network: {
    interfaces: [
      {
        iface: "eth0",
        state: "UP",
        mac: "00:11:22:33:44:55",
        mtu: 1500,
        ipv4: ["10.0.0.5"],
        ipv6: [],
        rx_bps: 1024,
        tx_bps: 2048,
        rx_pps: 5,
        tx_pps: 7,
        rx_errors: 0,
        tx_errors: 0,
        warming_up: false,
      },
    ],
    as_of: new Date().toISOString(),
  },
  processes: {
    total: 200,
    running: 1,
    sleeping: 199,
    zombie: 0,
    stopped: 0,
    other: 0,
    top_by_rss: [
      { pid: 1, comm: "init", user: "root", rss_kb: 1024, state: "S" },
    ],
  },
  services: {
    services: [
      {
        unit: "jabali-panel.service",
        active: "active",
        sub: "running",
        load_state: "loaded",
        memory_bytes: 50_000_000,
        tasks: 10,
        uptime_seconds: 3600,
      },
      {
        unit: "mariadb.service",
        active: "inactive",
        sub: "dead",
        load_state: "loaded",
        memory_bytes: 0,
        tasks: 0,
        uptime_seconds: 0,
      },
    ],
  },
};

test.describe("admin server status page", () => {
  test("renders all sections from one envelope", async ({ page }) => {
    await mockApi(page, { me: admin });

    await page.route("**/api/v1/admin/server-status*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(fakeEnvelope),
      }),
    );

    await signIn(page, admin);
    await page.goto("/jabali-admin/server-status");

    // Hostname banner.
    await expect(page.getByText("mx.jabali-panel.local")).toBeVisible();

    // NTP synced badge.
    await expect(page.getByText("NTP synced")).toBeVisible();

    // CPU meter renders the percentage.
    await expect(page.getByText("13%")).toBeVisible();

    // Disk row mount point. Use exact match because "/" alone matches
    // multiple cells (every disk's progress bar wrapper, percent
    // suffixes, etc.) under non-strict mode.
    await expect(page.getByRole("cell", { name: "/", exact: true })).toBeVisible();

    // Network row IP. SystemInfoCard's "IP address" descriptor also
    // surfaces the same address ("first non-loopback IPv4"), so a
    // bare getByText finds two matches. Scope to the NetworkTable's
    // AntD Tag (the row chip) where the spec actually wants to look.
    await expect(page.locator(".ant-tag", { hasText: "10.0.0.5" })).toBeVisible();

    // Critical alert from the envelope.
    await expect(page.getByText("mariadb.service is inactive")).toBeVisible();

    // Service rows render.
    // ServicesSummaryCard.prettyName strips the .service suffix and the
    // jabali- prefix so the rendered cell text is "panel", not the raw
    // unit name. Match the rendered shape, not the envelope's.
    await expect(page.getByRole("cell", { name: "panel", exact: true })).toBeVisible();
  });

  test("dashboard deep-links into server status", async ({ page }) => {
    await mockApi(page, { me: admin });
    await page.route("**/api/v1/admin/server-status*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ ...fakeEnvelope, alerts: [] }),
      }),
    );

    await signIn(page, admin);
    await page.goto("/jabali-admin/dashboard");

    await page.getByRole("link", { name: /View server status/i }).click();
    await expect(page).toHaveURL(/\/jabali-admin\/server-status/);
  });
});
