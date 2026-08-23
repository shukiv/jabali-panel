// EventsTab.test.tsx — JAB-381. Category grouping, counts, first-visit default,
// independent multi-expand, aria/keyboard, persistence + malformed storage, and
// unchanged toggle semantics. Only apiClient is mocked; real AntD Collapse/Table
// /Switch run.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { EventsTab } from "./EventsTab";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
};

const row = (
  kind: string,
  enabled: boolean,
  severity = "info" as "info" | "warning" | "error" | "critical",
) => ({ kind, label: kind, description: `${kind} desc`, severity, enabled, default_on: false });

// ssl: 2 events (1 enabled) | backups: 1 (1) | mail: 1 (0)
const SAMPLE = [
  row("cert.renew.fail", true, "error"),
  row("cert.renew.ok", false),
  row("backup.fail", true, "error"),
  row("mail.rbl.listed", false, "warning"),
];

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <App>
        <EventsTab />
      </App>
    </QueryClientProvider>,
  );
}

// The clickable AntD Collapse header carrying aria-expanded, found by its label.
const header = (name: string): HTMLElement =>
  screen.getByText(name).closest(".ant-collapse-header") as HTMLElement;

describe("EventsTab category grouping (JAB-381)", () => {
  beforeEach(() => {
    localStorage.clear();
    mocked.get.mockReset().mockResolvedValue({ data: { data: SAMPLE } });
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
  });

  it("renders only non-empty categories with live enabled/total counts", async () => {
    renderTab();
    await screen.findByText("SSL & Certificates");
    expect(screen.getByText("Backups")).toBeInTheDocument();
    expect(screen.getByText("Mail")).toBeInTheDocument();
    // empty categories are not rendered
    expect(screen.queryByText("Security")).not.toBeInTheDocument();
    expect(screen.queryByText("Services")).not.toBeInTheDocument();
    // header counts
    expect(screen.getByText("1 / 2 enabled")).toBeInTheDocument(); // ssl
    expect(screen.getByText("1 / 1 enabled")).toBeInTheDocument(); // backups
    expect(screen.getByText("0 / 1 enabled")).toBeInTheDocument(); // mail
  });

  it("expands the first non-empty category on first visit; others collapsed", async () => {
    renderTab();
    await screen.findByText("SSL & Certificates");
    expect(header("SSL & Certificates")).toHaveAttribute("aria-expanded", "true");
    expect(header("Backups")).toHaveAttribute("aria-expanded", "false");
    expect(header("Mail")).toHaveAttribute("aria-expanded", "false");
  });

  it("toggles categories independently — multiple stay open together", async () => {
    renderTab();
    await screen.findByText("SSL & Certificates");
    fireEvent.click(header("Backups"));
    expect(header("Backups")).toHaveAttribute("aria-expanded", "true");
    // the first-visit SSL panel stays open (not an accordion)
    expect(header("SSL & Certificates")).toHaveAttribute("aria-expanded", "true");
  });

  it("headers are keyboard-operable (Enter toggles) and expose aria-expanded", async () => {
    renderTab();
    await screen.findByText("SSL & Certificates");
    const mail = header("Mail");
    expect(mail).toHaveAttribute("aria-expanded", "false");
    fireEvent.keyDown(mail, { key: "Enter" });
    expect(mail).toHaveAttribute("aria-expanded", "true");
  });

  it("persists the expand choice across a remount", async () => {
    const first = renderTab();
    await screen.findByText("SSL & Certificates");
    fireEvent.click(header("Mail")); // open Mail in addition to SSL
    expect(header("Mail")).toHaveAttribute("aria-expanded", "true");
    // stored
    const stored = JSON.parse(localStorage.getItem("jabali.notifEvents.collapse.v1")!);
    expect(stored.v).toBe(1);
    expect(stored.open).toContain("mail");

    first.unmount();
    renderTab();
    await screen.findByText("SSL & Certificates");
    expect(header("Mail")).toHaveAttribute("aria-expanded", "true");
  });

  it("falls back to first-visit on malformed stored state", async () => {
    localStorage.setItem("jabali.notifEvents.collapse.v1", "{not valid json");
    renderTab();
    await screen.findByText("SSL & Certificates");
    // malformed → first-visit default (first non-empty expanded), no crash
    expect(header("SSL & Certificates")).toHaveAttribute("aria-expanded", "true");
    expect(header("Backups")).toHaveAttribute("aria-expanded", "false");
  });

  it("toggling a switch issues the unchanged PATCH and no other request shape", async () => {
    renderTab();
    await screen.findByText("SSL & Certificates");
    // SSL is open on first visit; toggle its first switch.
    const sslBody = header("SSL & Certificates").parentElement as HTMLElement;
    const firstSwitch = within(sslBody).getAllByRole("switch")[0];
    fireEvent.click(firstSwitch);
    await waitFor(() => expect(mocked.patch).toHaveBeenCalledTimes(1));
    const [url, body] = mocked.patch.mock.calls[0];
    expect(url).toMatch(/^\/admin\/settings\/notification-events\//);
    expect(body).toEqual({ enabled: expect.any(Boolean) });
  });
});
