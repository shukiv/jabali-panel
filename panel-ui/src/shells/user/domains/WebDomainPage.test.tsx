// WebDomainPage.test — GH #1543. The tenant Web Domain page: the URL :tab picks
// the active pane, an unknown/absent tab falls back to Overview, the breadcrumb
// trail ends at the domain name, a tab click navigates to that tab's URL, and a
// domain the caller can't load surfaces an error (never a blank scoped view).
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const navigate = vi.hoisted(() => vi.fn());
const setBreadcrumbs = vi.hoisted(() => vi.fn());
const domainQ = vi.hoisted(() => ({
  value: {
    data: {
      id: "d1",
      name: "site.tld",
      doc_root: "/home/alice/site.tld/public_html",
      ssl_state: "active",
      is_enabled: true,
      temp_url_enabled: false,
      temp_url: "",
      bot_challenge_include: false,
      reverse_proxy_port: null,
    } as Record<string, unknown> | undefined,
    isLoading: false,
    isError: false,
  },
}));
const patch = vi.hoisted(() => vi.fn());
const caps = vi.hoisted(() => ({ value: { tenant_domain_options_enabled: false, tenant_docroot_editable: false } }));

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => navigate };
});
vi.mock("../../../components/admin/BreadcrumbContext", () => ({
  useSetBreadcrumbs: (c: unknown) => setBreadcrumbs(c),
}));
vi.mock("../../../hooks/useQueries", () => ({
  useOneQuery: () => domainQ.value,
}));
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: caps.value }),
}));
vi.mock("../../../components/DomainNginxOptionsPanel", () => ({
  DomainNginxOptionsPanel: ({ domainId }: { domainId: string }) => <div>options-pane:{domainId}</div>,
}));
vi.mock("../../DomainSettingsButton", () => ({
  TenantNginxRulesPanel: ({ domain }: { domain: { id: string } }) => <div>rewrite-pane:{domain.id}</div>,
}));
vi.mock("../../../components/domains/DomainDocRootPanel", () => ({
  DomainDocRootPanel: ({ domainId }: { domainId: string }) => <div>docroot-pane:{domainId}</div>,
}));
vi.mock("../../../components/DomainCacheSection", () => ({
  DomainCacheSection: ({ domainId }: { domainId: string }) => <div>caching-pane:{domainId}</div>,
}));
vi.mock("../../admin/domains/DomainDirectoryPrivacySection", () => ({
  DomainDirectoryPrivacySection: ({ domainId }: { domainId: string }) => (
    <div>dirpriv-pane:{domainId}</div>
  ),
}));
vi.mock("../../../apiClient", () => ({ apiClient: { patch } }));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));

import { WebDomainPage } from "./WebDomainPage";

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/jabali-panel/domains/:id" element={<WebDomainPage />} />
          <Route path="/jabali-panel/domains/:id/:tab" element={<WebDomainPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  navigate.mockReset();
  setBreadcrumbs.mockReset();
  patch.mockReset();
  patch.mockResolvedValue({});
  caps.value = { tenant_domain_options_enabled: false, tenant_docroot_editable: false };
  domainQ.value = {
    data: {
      id: "d1",
      name: "site.tld",
      doc_root: "/home/alice/site.tld/public_html",
      ssl_state: "active",
      is_enabled: true,
      temp_url_enabled: false,
      temp_url: "",
      bot_challenge_include: false,
      reverse_proxy_port: null,
    },
    isLoading: false,
    isError: false,
  };
});

describe("WebDomainPage (GH #1543)", () => {
  it("defaults to the Overview pane with the domain facts and the two toggles", async () => {
    renderAt("/jabali-panel/domains/d1");
    expect(await screen.findByText("Preview URL")).toBeInTheDocument();
    expect(screen.getByText("Bot-detection challenge")).toBeInTheDocument();
    // Facts: the docroot is shown home-stripped.
    expect(screen.getByText("site.tld/public_html")).toBeInTheDocument();
  });

  it("falls back to Overview for an unknown tab", async () => {
    renderAt("/jabali-panel/domains/d1/bogus");
    expect(await screen.findByText("Preview URL")).toBeInTheDocument();
  });

  it("renders the Index pane when :tab=index and saves the chosen priority", async () => {
    renderAt("/jabali-panel/domains/d1/index");
    expect(await screen.findByText("Directory Index Priority")).toBeInTheDocument();
    fireEvent.click(screen.getByText("PHP first (index.php, then index.html)"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() =>
      expect(patch).toHaveBeenCalledWith("/domains/d1", { index_priority: "php_first" }),
    );
  });

  it("renders the Redirects pane when :tab=redirects and saves", async () => {
    renderAt("/jabali-panel/domains/d1/redirects");
    expect(await screen.findByText("Redirect Entire Domain")).toBeInTheDocument();
    // Whole-domain toggle starts OFF (fixture has no redirect_all_to). Saving
    // sends empty strings, not null — the GH #717 clear semantics.
    fireEvent.click(screen.getByRole("button", { name: "Save Redirects" }));
    await vi.waitFor(() =>
      expect(patch).toHaveBeenCalledWith("/domains/d1", {
        redirect_all_to: "",
        redirect_all_type: "",
        page_redirects: [],
      }),
    );
  });

  it("renders the Caching pane when :tab=caching", async () => {
    renderAt("/jabali-panel/domains/d1/caching");
    expect(await screen.findByText("caching-pane:d1")).toBeInTheDocument();
  });

  it("renders the Directory Privacy pane when :tab=directory-privacy", async () => {
    renderAt("/jabali-panel/domains/d1/directory-privacy");
    expect(await screen.findByText("dirpriv-pane:d1")).toBeInTheDocument();
  });

  it("sets the breadcrumb trail ending at the domain name", async () => {
    renderAt("/jabali-panel/domains/d1");
    await screen.findByText("Preview URL");
    const crumbs = setBreadcrumbs.mock.calls.at(-1)?.[0] as { title: string }[];
    expect(crumbs.map((c) => c.title)).toEqual(["Dashboard", "Web Domains", "site.tld"]);
  });

  it("navigates to the tab URL when a tab is clicked", async () => {
    renderAt("/jabali-panel/domains/d1");
    fireEvent.click(await screen.findByText("Caching"));
    expect(navigate).toHaveBeenCalledWith("/jabali-panel/domains/d1/caching");
  });

  it("toggling Preview URL PATCHes the domain", async () => {
    renderAt("/jabali-panel/domains/d1");
    fireEvent.click(await screen.findByLabelText("Preview URL"));
    await vi.waitFor(() =>
      expect(patch).toHaveBeenCalledWith("/domains/d1", { temp_url_enabled: true }),
    );
  });

  it("hides the cap-gated tabs when the caps are off", async () => {
    renderAt("/jabali-panel/domains/d1");
    await screen.findByText("Preview URL");
    expect(screen.getByText("Index Files")).toBeInTheDocument();
    expect(screen.queryByText("Domain options")).not.toBeInTheDocument();
    expect(screen.queryByText("Rewrite rules")).not.toBeInTheDocument();
    // "Document root" also labels an Overview fact, so assert the pane itself
    // (its stub marker) is absent rather than the ambiguous tab text.
    expect(screen.queryByText("docroot-pane:d1")).not.toBeInTheDocument();
  });

  it("shows the cap-gated tabs and renders their panes when the caps are on", async () => {
    caps.value = { tenant_domain_options_enabled: true, tenant_docroot_editable: true };
    renderAt("/jabali-panel/domains/d1/domain-options");
    expect(await screen.findByText("options-pane:d1")).toBeInTheDocument();
    // The other gated tabs are present in the strip.
    expect(screen.getByText("Rewrite rules")).toBeInTheDocument();
    expect(screen.getByText("Document root")).toBeInTheDocument();
  });

  it("falls back to Overview when a URL targets a cap-gated tab that is off", async () => {
    // options off (default) → /domain-options is not a visible tab.
    renderAt("/jabali-panel/domains/d1/domain-options");
    expect(await screen.findByText("Preview URL")).toBeInTheDocument();
    expect(screen.queryByText("options-pane:d1")).not.toBeInTheDocument();
  });

  it("surfaces an error when the domain can't be loaded", async () => {
    domainQ.value = { data: undefined, isLoading: false, isError: true };
    renderAt("/jabali-panel/domains/d1");
    expect(await screen.findByText("Domain not available")).toBeInTheDocument();
  });
});
