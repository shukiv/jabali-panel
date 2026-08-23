// EventsTab.test.tsx — category RAIL + DETAIL redesign. Rail lists only
// non-empty categories with live on/total counts; the detail pane shows the
// selected category, supports in-category filter + only-overridden, bulk
// enable/disable + reset, a category-spanning global search, and preserves the
// unchanged single-kind PATCH shape. Only apiClient is mocked; real AntD +
// i18n run.
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
  default_on = enabled,
) => ({ kind, label: `${kind} name`, description: `${kind} desc`, severity, enabled, default_on });

// ssl: 2 (1 on) | backups: 1 (1 on) | mail: 1 (0 on)
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

// The rail button for a category (accessible name includes name + count).
const catBtn = (name: RegExp): HTMLElement =>
  screen.getByRole("button", { name });

// The detail <section> is the sibling of the rail nav — scope row queries to it.
const detail = (): HTMLElement =>
  screen.getByRole("navigation").parentElement!.querySelector("section") as HTMLElement;

describe("EventsTab rail + detail redesign", () => {
  beforeEach(() => {
    localStorage.clear();
    mocked.get.mockReset().mockResolvedValue({ data: { data: SAMPLE } });
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
  });

  it("renders only non-empty categories in the rail with live on/total counts", async () => {
    renderTab();
    await screen.findByRole("button", { name: /SSL & Certificates/ });
    expect(catBtn(/Backups/)).toBeInTheDocument();
    expect(catBtn(/Mail/)).toBeInTheDocument();
    // empty categories are absent
    expect(screen.queryByText("Security")).not.toBeInTheDocument();
    expect(screen.queryByText("Services")).not.toBeInTheDocument();
    // counts
    expect(screen.getByText("1/2")).toBeInTheDocument(); // ssl
    expect(screen.getByText("1/1")).toBeInTheDocument(); // backups
    expect(screen.getByText("0/1")).toBeInTheDocument(); // mail
  });

  it("selects the first category on first visit and shows its events only", async () => {
    renderTab();
    await screen.findByRole("button", { name: /SSL & Certificates/ });
    const d = detail();
    expect(within(d).getByText("cert.renew.fail")).toBeInTheDocument();
    expect(within(d).getByText("cert.renew.ok")).toBeInTheDocument();
    // other categories' events are not in the detail pane
    expect(within(d).queryByText("backup.fail")).not.toBeInTheDocument();
    // header sub-count
    expect(within(d).getByText("1 of 2 enabled")).toBeInTheDocument();
  });

  it("switches the detail pane when another category is clicked", async () => {
    renderTab();
    await screen.findByRole("button", { name: /SSL & Certificates/ });
    fireEvent.click(catBtn(/Mail/));
    const d = detail();
    expect(within(d).getByText("mail.rbl.listed")).toBeInTheDocument();
    expect(within(d).queryByText("cert.renew.fail")).not.toBeInTheDocument();
  });

  it("toggling a switch issues the unchanged single-kind PATCH shape", async () => {
    renderTab();
    await screen.findByRole("button", { name: /SSL & Certificates/ });
    const firstSwitch = within(detail()).getAllByRole("switch")[0];
    fireEvent.click(firstSwitch);
    await waitFor(() => expect(mocked.patch).toHaveBeenCalledTimes(1));
    const [url, body] = mocked.patch.mock.calls[0];
    expect(url).toMatch(/^\/admin\/settings\/notification-events\//);
    expect(body).toEqual({ enabled: expect.any(Boolean) });
  });

  it("Enable all PATCHes only the disabled kinds in the category", async () => {
    renderTab();
    await screen.findByRole("button", { name: /SSL & Certificates/ });
    // ssl has 1 enabled + 1 disabled → Enable all sends exactly 1 PATCH
    fireEvent.click(screen.getByRole("button", { name: "Enable all" }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalledTimes(1));
    expect(mocked.patch.mock.calls[0][0]).toMatch(/cert\.renew\.ok$/);
    expect(mocked.patch.mock.calls[0][1]).toEqual({ enabled: true });
  });

  it("filters within the selected category", async () => {
    renderTab();
    await screen.findByRole("button", { name: /SSL & Certificates/ });
    const d = detail();
    fireEvent.change(within(d).getByLabelText("Filter in this category"), {
      target: { value: "renew.ok" },
    });
    expect(within(d).getByText("cert.renew.ok")).toBeInTheDocument();
    expect(within(d).queryByText("cert.renew.fail")).not.toBeInTheDocument();
  });

  it("global search spans every category and hides bulk controls", async () => {
    renderTab();
    await screen.findByRole("button", { name: /SSL & Certificates/ });
    fireEvent.change(screen.getByLabelText("Search all events"), {
      target: { value: "backup" },
    });
    const d = detail();
    expect(within(d).getByText("backup.fail")).toBeInTheDocument(); // from another category
    expect(within(d).queryByText("cert.renew.fail")).not.toBeInTheDocument();
    // bulk controls are hidden while global searching
    expect(screen.queryByRole("button", { name: "Enable all" })).not.toBeInTheDocument();
  });

  it("only-overridden filter narrows to changed-from-default events", async () => {
    // cert.renew.fail enabled but default_on=false → overridden; others aligned.
    mocked.get.mockResolvedValue({
      data: {
        data: [
          row("cert.renew.fail", true, "error", false),
          row("cert.renew.ok", false, "info", false),
        ],
      },
    });
    renderTab();
    await screen.findByRole("button", { name: /SSL & Certificates/ });
    const d = detail();
    fireEvent.click(within(d).getByLabelText("Only overridden"));
    expect(within(d).getByText("cert.renew.fail")).toBeInTheDocument();
    expect(within(d).queryByText("cert.renew.ok")).not.toBeInTheDocument();
  });

  it("persists the selected category across a remount", async () => {
    const first = renderTab();
    await screen.findByRole("button", { name: /SSL & Certificates/ });
    fireEvent.click(catBtn(/Mail/));
    expect(within(detail()).getByText("mail.rbl.listed")).toBeInTheDocument();
    expect(localStorage.getItem("jabali.notifEvents.selCat.v1")).toBe("mail");

    first.unmount();
    renderTab();
    await screen.findByRole("button", { name: /SSL & Certificates/ });
    expect(within(detail()).getByText("mail.rbl.listed")).toBeInTheDocument();
  });
});
