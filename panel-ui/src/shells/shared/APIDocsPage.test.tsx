// APIDocsPage.test.tsx — GH #2244. The API docs list ~90 sections and ~770
// endpoints. The page shows one section at a time, picked from a list (a
// rail on wide screens, a searchable select on narrow ones), and a search
// that spans every endpoint. The rail replaces an antd Anchor that scrolled
// the window, which did nothing in the admin shell (its content column
// scrolls instead). Only apiClient is mocked; real AntD runs.
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../apiClient", () => ({ apiClient: { get: vi.fn() } }));

import { apiClient } from "../../apiClient";
import { APIDocsPage } from "./APIDocsPage";

const get = apiClient.get as unknown as ReturnType<typeof vi.fn>;

// Declared order is Backups, then DNS; an untagged endpoint lands in "Other".
const SPEC = {
  info: { title: "Jabali API", version: "1.0" },
  tags: [
    { name: "Backups", description: "Backup jobs." },
    { name: "DNS", description: "Zones and records." },
  ],
  paths: {
    "/dns/zones": { get: { tags: ["DNS"], summary: "List zones" } },
    "/dns/zones/{id}/records": { post: { tags: ["DNS"], summary: "Add a record" } },
    "/backups": { get: { tags: ["Backups"], summary: "List backups" } },
    "/ping": { get: { summary: "Health check" } },
  },
};

const realMatchMedia = window.matchMedia;

// AntD's breakpoints read matchMedia; answer as a window `width` px wide.
function setWidth(width: number) {
  window.matchMedia = ((query: string) => {
    const min = /min-width:\s*(\d+)px/.exec(query);
    const max = /max-width:\s*(\d+(?:\.\d+)?)px/.exec(query);
    const matches = (!min || width >= Number(min[1])) && (!max || width <= Number(max[1]));
    return {
      matches,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    };
  }) as typeof window.matchMedia;
}

const rail = () => screen.getByRole("navigation", { name: "API sections" });
// The section buttons; the filter's clear button has no text.
const railItems = () =>
  within(rail())
    .getAllByRole("button")
    .map((b) => b.textContent)
    .filter(Boolean);

beforeEach(() => {
  get.mockReset();
  get.mockResolvedValue({ data: SPEC });
  setWidth(1280);
  window.history.replaceState(null, "", "/jabali-admin/api-docs");
});

afterEach(() => {
  window.matchMedia = realMatchMedia;
});

describe("APIDocsPage sections (GH #2244)", () => {
  it("lists the sections in the spec's order with their counts, and shows only the first", async () => {
    render(<APIDocsPage />);
    await screen.findByText("/backups");

    expect(railItems()).toEqual(["Backups1", "DNS2", "Other1"]);
    expect(screen.queryByText("/dns/zones")).not.toBeInTheDocument();
    expect(screen.getByText("Backup jobs.")).toBeInTheDocument();
  });

  it("picking a section shows its endpoints and puts it in the address", async () => {
    render(<APIDocsPage />);
    await screen.findByText("/backups");

    fireEvent.click(within(rail()).getByRole("button", { name: /DNS/ }));

    expect(await screen.findByText("/dns/zones")).toBeInTheDocument();
    expect(screen.getByText("/dns/zones/{id}/records")).toBeInTheDocument();
    expect(screen.queryByText("/backups")).not.toBeInTheDocument();
    expect(window.location.hash).toBe("#tag-dns");
    expect(within(rail()).getByRole("button", { name: /DNS/ })).toHaveAttribute("aria-current", "true");
  });

  it("opens the section the address names", async () => {
    window.history.replaceState(null, "", "/jabali-admin/api-docs#tag-dns");
    render(<APIDocsPage />);
    expect(await screen.findByText("/dns/zones")).toBeInTheDocument();
    expect(screen.queryByText("/backups")).not.toBeInTheDocument();
  });

  it("the search spans every section and says where each match lives", async () => {
    render(<APIDocsPage />);
    await screen.findByText("/backups");

    fireEvent.change(screen.getByRole("textbox", { name: "Search all endpoints" }), { target: { value: "list" } });

    // "List zones" (DNS) and "List backups" (Backups), each under its section.
    expect(await screen.findByText("/dns/zones")).toBeInTheDocument();
    expect(screen.getByText("/backups")).toBeInTheDocument();
    expect(screen.queryByText("/dns/zones/{id}/records")).not.toBeInTheDocument();
    expect(screen.getByText("2 of 4 endpoints match")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /DNS/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Backups/ })).toBeInTheDocument();
  });

  it("the search matches the method and the path too, and clearing it returns to the section", async () => {
    render(<APIDocsPage />);
    await screen.findByText("/backups");
    const search = screen.getByRole("textbox", { name: "Search all endpoints" });

    fireEvent.change(search, { target: { value: "post records" } });
    expect(await screen.findByText("/dns/zones/{id}/records")).toBeInTheDocument();
    expect(screen.queryByText("/backups")).not.toBeInTheDocument();

    fireEvent.change(search, { target: { value: "nothing-like-this" } });
    expect(await screen.findByText("No endpoint matches your search")).toBeInTheDocument();

    fireEvent.change(search, { target: { value: "" } });
    expect(await screen.findByText("/backups")).toBeInTheDocument();
    expect(screen.queryByText("/dns/zones")).not.toBeInTheDocument();
  });

  it("the list of sections has its own filter", async () => {
    render(<APIDocsPage />);
    await screen.findByText("/backups");

    fireEvent.change(screen.getByRole("textbox", { name: "Filter sections" }), { target: { value: "dn" } });
    await waitFor(() => expect(railItems()).toEqual(["DNS2"]));
  });

  it("on a narrow screen the sections are a searchable select instead of a rail", async () => {
    setWidth(390);
    render(<APIDocsPage />);
    await screen.findByText("/backups");

    expect(screen.queryByRole("navigation", { name: "API sections" })).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "API section" })).toBeInTheDocument();
  });
});
