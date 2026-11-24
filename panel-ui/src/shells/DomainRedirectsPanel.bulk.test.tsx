// DomainRedirectsPanel.bulk.test.tsx — page redirects at migrated-site scale.
//
// An .htaccess import fills the Redirects list with hundreds of entries that
// carry keep_query / trailing_slash. Saving from the panel must send those
// flags back (the save rebuilds each row, so a missing field would silently
// drop them), the list must page instead of rendering every card, and the
// importer must add its redirects to the list without duplicating a path.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DomainRedirectsPanel, type PageRedirect } from "./DomainRedirectsPanel";
import { DomainNginxSection } from "./DomainSettingsButton";

vi.mock("../apiClient", () => ({ apiClient: { patch: vi.fn(), post: vi.fn(), get: vi.fn() } }));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as {
  patch: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  get: ReturnType<typeof vi.fn>;
};

const wrap = (node: ReactNode) =>
  render(
    <QueryClientProvider client={new QueryClient()}>
      <App>{node}</App>
    </QueryClientProvider>,
  );

const entry = (source: string): PageRedirect => ({
  source,
  destination: "https://shop.example/p?aid=1",
  type: "307",
  active: true,
  wildcard: false,
  keep_query: true,
  trailing_slash: true,
});

describe("page redirects at scale", () => {
  beforeEach(() => {
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
    mocked.post.mockReset();
    mocked.get.mockReset();
  });

  it("saves keep_query and trailing_slash back", async () => {
    wrap(<DomainRedirectsPanel domain={{ id: "d1", name: "x.example", page_redirects: [entry("/go")] }} />);
    fireEvent.click(screen.getByText("Save Redirects"));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    const [, body] = mocked.patch.mock.calls[0];
    expect(body.page_redirects).toEqual([entry("/go")]);
  });

  it("pages a long list and filters it", () => {
    const many = Array.from({ length: 120 }, (_, i) => entry(`/s${i}`));
    wrap(<DomainRedirectsPanel domain={{ id: "d1", name: "x.example", page_redirects: many }} />);
    expect(screen.getByText("120 / 2000")).toBeInTheDocument();
    expect(screen.getAllByPlaceholderText("/old-page")).toHaveLength(50);
    fireEvent.change(screen.getByPlaceholderText("Filter by path or URL"), { target: { value: "/s119" } });
    const shown = screen.getAllByPlaceholderText("/old-page") as HTMLInputElement[];
    expect(shown.map((i) => i.value)).toEqual(["/s119"]);
  });

  it("adds imported redirects to the list it reads fresh, skipping a path it already redirects", async () => {
    // The domain row the section got has no page_redirects (a list row); the
    // import must merge into the stored list, never replace it.
    mocked.get.mockResolvedValue({ data: { id: "d1", page_redirects: [entry("/a")] } });
    mocked.post.mockResolvedValue({
      data: { rules: [], warnings: [], notes: [], redirects: [entry("/a/"), entry("/b")] },
    });
    wrap(
      <DomainNginxSection
        domain={{ id: "d1", name: "x.example", nginx_rules: [] }}
      />,
    );
    fireEvent.click(screen.getByText("Import .htaccess"));
    fireEvent.change(await screen.findByPlaceholderText(/paste \.htaccess here/i), {
      target: { value: 'Redirect 307 "/b" "https://shop.example/p?aid=1"' },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Convert$/i }));
    fireEvent.click(await screen.findByRole("button", { name: /Add to Redirects/i }));

    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    expect(mocked.get).toHaveBeenCalledWith("/domains/d1");
    const [url, body] = mocked.patch.mock.calls[0];
    expect(url).toBe("/domains/d1");
    // "/a" with trailing_slash already answers "/a/".
    expect(body).toEqual({ page_redirects: [entry("/a"), entry("/b")] });
  });
});
