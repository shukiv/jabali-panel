// SystemUpdatesPage.autoupdate.test.tsx — GH #2221. The Automatic Updates
// switches only changed a draft: nothing was stored until Save at the foot of
// the card, so turning OS security updates off (even through the confirm's
// "Disable security updates") and refreshing showed it on again. Each change
// now applies at once.
import type { ReactNode } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

type MasonryItem = { key: string; children?: ReactNode };

vi.mock("antd", async (importOriginal) => {
  const actual = await importOriginal<typeof import("antd")>();
  return {
    ...actual,
    // happy-dom has no layout; render the cards in a plain list.
    Masonry: ({ items = [] }: { items?: MasonryItem[] }) => (
      <div>{items.map((it) => <div key={it.key}>{it.children}</div>)}</div>
    ),
  };
});

const initial = {
  id: 1,
  apt_enabled: true,
  apt_optout_acknowledged: false,
  apt_time: "03:30",
  jabali_enabled: false,
  jabali_time: "04:30",
  security_enabled: true,
};
const store = vi.hoisted(() => ({ cfg: {} as Record<string, unknown>, failGet: false }));
const put = vi.hoisted(() => vi.fn());

vi.mock("../../../apiClient", () => ({
  apiClient: {
    get: vi.fn((url: string) => {
      if (url === "/admin/updates/autoupdate") {
        if (store.failGet) return Promise.reject(new Error("network"));
        return Promise.resolve({ data: { ...store.cfg } });
      }
      if (url.includes("/admin/updates/history")) return Promise.resolve({ data: { items: [], total: 0 } });
      if (url === "/admin/updates/apt/check") return Promise.resolve({ data: { packages: [], total: 0 } });
      if (url === "/users") return Promise.resolve({ data: { data: [] } });
      return Promise.resolve({ data: {} });
    }),
    post: vi.fn(() => Promise.resolve({ data: {} })),
    put,
    patch: vi.fn(() => Promise.resolve({ data: {} })),
  },
}));

vi.mock("./RepairCard", () => ({ RepairCard: () => <div>Repair Center</div> }));
vi.mock("./DomainRepairCard", () => ({ DomainRepairCard: () => <div>Domain repair</div> }));
vi.mock("./DNSOrphanRepairCard", () => ({ DNSOrphanRepairCard: () => <div>PowerDNS orphan records</div> }));

import { SystemUpdatesPage } from "./SystemUpdatesPage";

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <SystemUpdatesPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// The switch on the row titled `label`.
async function rowSwitch(label: string) {
  const title = await screen.findByText(label);
  return within(title.closest(".ant-row") as HTMLElement).getByRole("switch");
}

beforeEach(() => {
  store.cfg = { ...initial };
  store.failGet = false;
  put.mockReset();
  // The server stores the body and answers with the saved row.
  put.mockImplementation((_url: string, body: Record<string, unknown>) => {
    store.cfg = { ...body };
    return Promise.resolve({ data: { ...body } });
  });
});

describe("Automatic Updates applies each change (GH #2221)", () => {
  it("turning OS security updates off saves it once the risk is confirmed", async () => {
    renderPage();
    fireEvent.click(await rowSwitch("OS security updates"));
    // The confirm comes first; nothing is saved before it.
    expect(await screen.findByText("Disable OS security auto-updates?")).toBeInTheDocument();
    expect(put).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("checkbox", { name: /I understand the risk/ }));
    fireEvent.click(screen.getByRole("button", { name: "Disable security updates" }));

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(put).toHaveBeenCalledWith(
      "/admin/updates/autoupdate",
      expect.objectContaining({ apt_enabled: false, apt_optout_acknowledged: true }),
    );
    await waitFor(async () => expect(await rowSwitch("OS security updates")).not.toBeChecked());
  });

  it("cancelling the confirm saves nothing and leaves the switch on", async () => {
    renderPage();
    fireEvent.click(await rowSwitch("OS security updates"));
    expect(await screen.findByText("Disable OS security auto-updates?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(put).not.toHaveBeenCalled();
    expect(await rowSwitch("OS security updates")).toBeChecked();
  });

  it("turning OS security updates on saves it at once and clears the opt-out", async () => {
    store.cfg = { ...initial, apt_enabled: false, apt_optout_acknowledged: true };
    renderPage();
    const sw = await rowSwitch("OS security updates");
    expect(sw).not.toBeChecked();
    fireEvent.click(sw);

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(put).toHaveBeenCalledWith(
      "/admin/updates/autoupdate",
      expect.objectContaining({ apt_enabled: true, apt_optout_acknowledged: false }),
    );
  });

  it("the other switches save at once too, and there is no Save button to miss", async () => {
    renderPage();
    fireEvent.click(await rowSwitch("Security advisories"));

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    // The whole row goes out, so the other settings are kept.
    expect(put).toHaveBeenCalledWith(
      "/admin/updates/autoupdate",
      expect.objectContaining({ security_enabled: false, apt_enabled: true, apt_time: "03:30" }),
    );
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
  });

  it("shows the saved row even when the reload after the save fails", async () => {
    renderPage();
    const sw = await rowSwitch("Security advisories");
    put.mockImplementation((_url: string, body: Record<string, unknown>) => {
      store.failGet = true;
      return Promise.resolve({ data: { ...body } });
    });
    fireEvent.click(sw);

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    await waitFor(async () => expect(await rowSwitch("Security advisories")).not.toBeChecked());
  });

  it("locks the other controls while a save runs, so a second change cannot undo the first", async () => {
    renderPage();
    const advisories = await rowSwitch("Security advisories");
    put.mockImplementation(() => new Promise(() => {}));
    fireEvent.click(advisories);

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(await rowSwitch("Jabali panel self-update")).toBeDisabled();
    expect(await rowSwitch("OS security updates")).toBeDisabled();
  });
});
