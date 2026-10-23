// UserAPITokensPage.layout.test.tsx — GH #2244. The Personal API Tokens page is
// laid out like Notifications: the page's heading, then one card whose tabs
// hold the tokens, the two setup guides and the Automation API docs, with no
// card inside it. A link to a docs section (#tag-<slug>) opens the docs tab.
import { App } from "antd";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { UserAPITokensPage } from "./UserAPITokensPage";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const get = apiClient.get as unknown as ReturnType<typeof vi.fn>;

const SPEC = {
  info: { title: "Jabali API", version: "1.0" },
  tags: [{ name: "DNS" }, { name: "Backups" }],
  paths: {
    "/dns/zones": { get: { tags: ["DNS"], summary: "List zones" } },
    "/backups": { get: { tags: ["Backups"], summary: "List backups" } },
  },
};

const realMatchMedia = window.matchMedia;

// AntD's breakpoints read matchMedia; answer as a desktop window.
function setDesktop() {
  window.matchMedia = ((query: string) => {
    const max = /max-width:\s*(\d+(?:\.\d+)?)px/.exec(query);
    return {
      matches: !max,
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

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  get.mockImplementation((url: string) =>
    Promise.resolve({ data: url === "/_meta/openapi.json" ? SPEC : { items: [] } }),
  );
  setDesktop();
  window.history.replaceState(null, "", "/jabali-panel/api-tokens");
});

afterEach(() => {
  window.matchMedia = realMatchMedia;
});

function renderPage() {
  return render(
    <App>
      <UserAPITokensPage />
    </App>,
  );
}

describe("Personal API Tokens page layout (GH #2244)", () => {
  it("has the page's heading above one card of tabs", async () => {
    const { container } = renderPage();

    expect(screen.getByRole("heading", { level: 3, name: /Personal API Tokens/ })).toBeInTheDocument();
    const cards = container.querySelectorAll(".ant-card");
    expect(cards).toHaveLength(1);
    const card = cards[0] as HTMLElement;
    for (const name of ["Tokens", "Dynamic DNS", "MCP", "Automation API"]) {
      expect(within(card).getByRole("tab", { name })).toBeInTheDocument();
    }
    expect(within(card).getByRole("button", { name: /New token/ })).toBeInTheDocument();
  });

  it("shows the API docs inside the same card, with no card of their own", async () => {
    const { container } = renderPage();
    fireEvent.click(screen.getByRole("tab", { name: "Automation API" }));

    const nav = await screen.findByRole("navigation", { name: "API sections" });
    const cards = container.querySelectorAll(".ant-card");
    expect(cards).toHaveLength(1);
    expect(cards[0]).toContainElement(nav);
    expect(cards[0]).toContainElement(screen.getByRole("textbox", { name: "Search all endpoints" }));
  });

  it("the setup guides are sections of the card, not cards inside it", async () => {
    const { container } = renderPage();

    fireEvent.click(screen.getByRole("tab", { name: "Dynamic DNS" }));
    expect(await screen.findByRole("heading", { level: 5, name: /Dynamic DNS/ })).toBeInTheDocument();
    expect(container.querySelectorAll(".ant-card")).toHaveLength(1);

    fireEvent.click(screen.getByRole("tab", { name: "MCP" }));
    expect(await screen.findByRole("heading", { level: 5, name: /AI assistant/ })).toBeInTheDocument();
    expect(container.querySelectorAll(".ant-card")).toHaveLength(1);
  });

  it("a tab keeps its state while another tab is open", async () => {
    renderPage();

    fireEvent.click(screen.getByRole("tab", { name: "MCP" }));
    fireEvent.click(await screen.findByRole("button", { name: /Next/ }));
    expect(screen.getByRole("button", { name: /Back/ })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Automation API" }));
    await screen.findByRole("navigation", { name: "API sections" });

    fireEvent.click(screen.getByRole("tab", { name: "Tokens" }));
    fireEvent.click(screen.getByRole("tab", { name: "MCP" }));
    expect(screen.getByRole("button", { name: /Back/ })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Automation API" }));
    expect(screen.getByRole("navigation", { name: "API sections" })).toBeInTheDocument();
    expect(get.mock.calls.filter(([url]) => url === "/_meta/openapi.json")).toHaveLength(1);
  });

  it("a link to a docs section opens the Automation API tab on that section", async () => {
    window.history.replaceState(null, "", "/jabali-panel/api-tokens#tag-dns");
    renderPage();

    expect(await screen.findByText("/dns/zones")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Automation API" })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByText("/backups")).not.toBeInTheDocument();
  });
});
