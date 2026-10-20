// ExtraBlocklistsCard.test.tsx — the admin's extra IP blocklists: shows the
// catalog with what is on and what the last run did, asks before adding a
// list, and PUTs the chosen keys and the admin's own list URLs. Only
// apiClient is mocked.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { ExtraBlocklistsCard } from "./ExtraBlocklistsCard";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

const lists = (on: string[]) =>
  ["firehol_level1", "firehol_level2", "firehol_level3"].map((key, i) => ({
    key,
    name: `FireHOL level ${i + 1}`,
    description: `level ${i + 1} description`,
    enabled: on.includes(key),
  }));

const result = (key: string, extra: Record<string, unknown> = {}) => ({
  key,
  enabled: true,
  at: "2026-10-20T04:00:00Z",
  fetched: 4633,
  imported: 4600,
  dropped_reserved: 11,
  dropped_protected: 2,
  dropped_duplicate: 0,
  dropped_over_cap: 0,
  invalid: 0,
  ...extra,
});

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ExtraBlocklistsCard />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mocked.get.mockReset();
  mocked.put.mockReset();
  mocked.post.mockReset();
});

describe("ExtraBlocklistsCard", () => {
  it("shows which lists are on and what the last run did", async () => {
    mocked.get.mockResolvedValue({
      data: {
        lists: lists(["firehol_level1", "firehol_level2"]),
        status: {
          running: false,
          last_run: "2026-10-20T04:00:00Z",
          lists: [result("firehol_level1"), result("firehol_level2", { error: "download failed: HTTP 503", imported: 0 })],
        },
      },
    });
    renderCard();
    const l1 = await screen.findByRole("checkbox", { name: "FireHOL level 1" });
    expect(l1).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "FireHOL level 3" })).not.toBeChecked();
    expect(screen.getByText(/4,600 addresses or ranges banned; 13 left out \(11 private or reserved, 2 this server's or allowlisted\)/)).toBeInTheDocument();
    expect(screen.getByText(/Last run failed: download failed: HTTP 503/)).toBeInTheDocument();
  });

  // On a phone the header has room for the title and one button; the last-run
  // time beside them squeezed the title down to "E…".
  it("keeps the last-run time out of the card header", async () => {
    mocked.get.mockResolvedValue({
      data: {
        lists: lists(["firehol_level1"]),
        status: { running: true, last_run: "2026-10-20T04:00:00Z", lists: [result("firehol_level1")] },
      },
    });
    const { container } = renderCard();
    expect(await screen.findByText(/^Last run/)).toBeInTheDocument();
    expect(screen.getByText("Updating")).toBeInTheDocument();
    const header = container.querySelector(".ant-card-head");
    expect(header?.textContent).not.toMatch(/Last run|Updating/);
    expect(header?.textContent).toMatch(/Download now/);
  });

  it("asks before adding a list, then PUTs the chosen keys", async () => {
    mocked.get.mockResolvedValue({ data: { lists: lists([]), status: null } });
    mocked.put.mockResolvedValue({ data: { lists: lists(["firehol_level1"]), status: null } });
    renderCard();
    fireEvent.click(await screen.findByRole("checkbox", { name: "FireHOL level 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocked.put).not.toHaveBeenCalled();
    expect(await screen.findByText("Ban these lists on every port?")).toBeInTheDocument();
    const confirm = screen.getAllByRole("button", { name: "Save" }).at(-1)!;
    fireEvent.click(confirm);
    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/admin/security/crowdsec/blocklists/feeds", {
        enabled: ["firehol_level1"],
        custom: [],
      }),
    );
  });

  it("turns a list off without asking", async () => {
    mocked.get.mockResolvedValue({ data: { lists: lists(["firehol_level2"]), status: null } });
    mocked.put.mockResolvedValue({ data: { lists: lists([]), status: null } });
    renderCard();
    fireEvent.click(await screen.findByRole("checkbox", { name: "FireHOL level 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/admin/security/crowdsec/blocklists/feeds", { enabled: [], custom: [] }),
    );
  });

  it("says what is never banned and that lists cover every port", async () => {
    mocked.get.mockResolvedValue({ data: { lists: lists([]), status: null } });
    renderCard();
    expect(await screen.findByText(/on every port: websites, mail and SSH/)).toBeInTheDocument();
    expect(screen.getByText(/allowlisted addresses are\s+never banned/)).toBeInTheDocument();
  });

  it("adds the admin's own list URLs, asking first", async () => {
    mocked.get.mockResolvedValue({ data: { lists: lists([]), custom: [], status: null } });
    mocked.put.mockResolvedValue({ data: { lists: lists([]), custom: [], status: null } });
    renderCard();
    const box = await screen.findByRole("textbox", { name: "Your own blocklist URLs" });
    fireEvent.change(box, { target: { value: " https://lists.example.org/a.txt \n\nhttps://lists.example.org/a.txt" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Ban these lists on every port?")).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Save" }).at(-1)!);
    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/admin/security/crowdsec/blocklists/feeds", {
        enabled: [],
        custom: ["https://lists.example.org/a.txt"],
      }),
    );
  });

  it("refuses an own list that is not an https URL", async () => {
    mocked.get.mockResolvedValue({ data: { lists: lists([]), custom: [], status: null } });
    renderCard();
    const box = await screen.findByRole("textbox", { name: "Your own blocklist URLs" });
    fireEvent.change(box, { target: { value: "http://lists.example.org/a.txt" } });
    expect(screen.getByText(/Not an https:\/\/ URL: http:\/\/lists.example.org\/a.txt/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("shows what the last run did with an own list", async () => {
    const url = "https://lists.example.org/a.txt";
    mocked.get.mockResolvedValue({
      data: {
        lists: lists([]),
        custom: [{ url, key: "custom-0123456789ab" }],
        status: { running: false, last_run: "2026-10-20T04:00:00Z", lists: [result("custom-0123456789ab", { url })] },
      },
    });
    renderCard();
    expect(await screen.findByRole("textbox", { name: "Your own blocklist URLs" })).toHaveValue(url);
    expect(screen.getByText(/4,600 addresses or ranges banned/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Download now/ })).toBeEnabled();
  });
});
