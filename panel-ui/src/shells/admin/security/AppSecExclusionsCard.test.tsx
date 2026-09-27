// AppSecExclusionsCard.test.tsx — GH #1649. The triage table loads only on
// request (the agent inspects every alert), an "Exclude…" action prefills the
// drawer with what the server accepts, and a server refusal stays visible in
// the drawer. Only apiClient is mocked; the real AntD widgets render.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AppSecExclusionsPanel } from "./AppSecExclusionsCard";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

const EVENTS = "/admin/security/crowdsec/appsec/events";
const EXCLUSIONS = "/admin/security/crowdsec/appsec/exclusions";

const eventsBody = {
  patterns: [
    {
      rule_ids: ["901340", "942100", "949110"],
      detections: ["942100"],
      other: [],
      infra: [
        { id: "901340", note: "body-inspection enabler — scores nothing, never exclude this" },
        { id: "949110", note: "anomaly threshold reached — the blocker, not a detection" },
      ],
      host: "Shop.Example.com:8443",
      uri: "/cart/add?id=1",
      count: 4,
      distinct_ips: 3,
      first_at: "2026-09-27T10:00:00Z",
      last_at: "2026-09-27T11:00:00Z",
    },
    {
      rule_ids: ["901340", "949110"],
      detections: [],
      other: [],
      infra: [],
      host: "other.example.com",
      uri: "/",
      count: 1,
      distinct_ips: 1,
      first_at: "2026-09-27T10:00:00Z",
      last_at: "2026-09-27T10:00:00Z",
    },
    {
      rule_ids: ["901340", "2410974272"],
      detections: [],
      other: [{ id: "2410974272", note: "not a CRS detection rule" }],
      infra: [],
      host: "native.example.com",
      uri: "/",
      count: 7,
      distinct_ips: 4,
      first_at: "2026-09-27T10:00:00Z",
      last_at: "2026-09-27T10:00:00Z",
    },
  ],
  inline_blocks: [],
  alerts_scanned: 5,
  events_count: 5,
  inline_count: 0,
  limit: 25,
};

const exclusionsBody = {
  data: [
    {
      id: "01HXA",
      host: "forum.example.com",
      uri_prefix: "/api/",
      rule_id: "920450",
      note: "Flarum X-HTTP-Method-Override (CRS 920450 false positive), managed for Flarum install inst1 (GH #1650)",
      created_at: "2026-09-27T10:00:00Z",
      updated_at: "2026-09-27T10:00:00Z",
      managed_install_id: "inst1",
    },
  ],
  total: 1,
};

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <AppSecExclusionsPanel />
      </App>
    </QueryClientProvider>,
  );
}

const eventCalls = () => mocked.get.mock.calls.filter(([url]) => url === EVENTS);

describe("GH #1649 — AppSec exclusions panel", () => {
  beforeEach(() => {
    mocked.get.mockReset().mockImplementation(async (url: string) => {
      if (url === EVENTS) return { data: eventsBody };
      if (url === EXCLUSIONS) return { data: exclusionsBody };
      throw new Error(`unexpected GET ${url}`);
    });
    mocked.post.mockReset();
    mocked.delete.mockReset();
  });

  it("loads recent blocks only when asked", async () => {
    renderPanel();
    await screen.findByText("forum.example.com");
    expect(eventCalls()).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: /Load blocks/i }));
    await screen.findByText("Shop.Example.com:8443");
    expect(eventCalls()).toHaveLength(1);
    expect(eventCalls()[0][1]).toMatchObject({ params: { limit: 25 } });
  });

  it("marks a row the panel manages for a Flarum install", async () => {
    renderPanel();
    const row = (await screen.findByText("forum.example.com")).closest("tr") as HTMLElement;
    expect(within(row).getByText("Flarum")).toBeInTheDocument();
  });

  it("offers no exclusion for a block only infrastructure rules matched", async () => {
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: /Load blocks/i }));
    const row = (await screen.findByText("other.example.com")).closest("tr") as HTMLElement;
    expect(within(row).getByRole("button", { name: /Exclude/i })).toBeDisabled();
  });

  it("offers no exclusion when only rules outside the CRS range scored", async () => {
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: /Load blocks/i }));
    const row = (await screen.findByText("native.example.com")).closest("tr") as HTMLElement;
    expect(within(row).getByText("2410974272")).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: /Exclude/i })).toBeDisabled();
  });

  it("prefills the drawer from a block and posts what the server accepts", async () => {
    mocked.post.mockResolvedValue({
      data: { exclusion: { id: "01HXB" }, apply: { changed: true, reloaded: true } },
    });
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: /Load blocks/i }));
    const row = (await screen.findByText("Shop.Example.com:8443")).closest("tr") as HTMLElement;
    fireEvent.click(within(row).getByRole("button", { name: /Exclude/i }));

    const host = (await screen.findByLabelText("Host")) as HTMLInputElement;
    expect(host.value).toBe("shop.example.com");
    expect((screen.getByLabelText("Path prefix") as HTMLInputElement).value).toBe("/cart/add");
    expect((screen.getByLabelText("Rule id") as HTMLInputElement).value).toBe("942100");

    fireEvent.click(screen.getByRole("button", { name: /^Add$/ }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalledTimes(1));
    const [url, body] = mocked.post.mock.calls[0];
    expect(url).toBe(EXCLUSIONS);
    expect(body).toMatchObject({ host: "shop.example.com", uri_prefix: "/cart/add", rule_id: "942100" });
  });

  it("keeps a server refusal visible in the drawer", async () => {
    mocked.post.mockRejectedValue(
      new Error("rule 949110 is the anomaly-score blocker — excluding it disables the WAF for that path"),
    );
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: /Add exclusion/i }));
    fireEvent.change(await screen.findByLabelText("Host"), { target: { value: "blog.example.com" } });
    fireEvent.change(screen.getByLabelText("Path prefix"), { target: { value: "/x/" } });
    fireEvent.change(screen.getByLabelText("Rule id"), { target: { value: "949110" } });
    fireEvent.click(screen.getByRole("button", { name: /^Add$/ }));

    expect(await screen.findByText(/anomaly-score blocker/)).toBeInTheDocument();
    expect(screen.getByLabelText("Host")).toBeInTheDocument();
  });

  it("refuses a host with a port before calling the server", async () => {
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: /Add exclusion/i }));
    fireEvent.change(await screen.findByLabelText("Host"), { target: { value: "blog.example.com:8443" } });
    fireEvent.change(screen.getByLabelText("Path prefix"), { target: { value: "/x/" } });
    fireEvent.change(screen.getByLabelText("Rule id"), { target: { value: "942100" } });
    fireEvent.click(screen.getByRole("button", { name: /^Add$/ }));

    expect(await screen.findByText(/no port/i)).toBeInTheDocument();
    expect(mocked.post).not.toHaveBeenCalled();
  });
});
