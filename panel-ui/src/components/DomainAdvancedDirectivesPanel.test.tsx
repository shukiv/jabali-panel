// DomainAdvancedDirectivesPanel.test — GH #1624 / ADR-0169 Phase 4b. Tenant raw
// "advanced directives" editor: loads the domain's current directives, PATCHes
// nginx_tenant_directives on save, and — the key UX guarantee — surfaces the
// backend's exact rejection reason (the 400 `detail` from
// ValidateNginxDirectivesTenant) rather than a generic toast.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));

const errorToast = vi.hoisted(() => vi.fn());
const successToast = vi.hoisted(() => vi.fn());
vi.mock("../lib/feedback", () => ({
  feedback: { message: { success: successToast, error: errorToast, warning: vi.fn() } },
}));

import { apiClient } from "../apiClient";
import { DomainAdvancedDirectivesPanel } from "./DomainAdvancedDirectivesPanel";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPatch = apiClient.patch as ReturnType<typeof vi.fn>;

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DomainAdvancedDirectivesPanel domainId="d1" />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockGet.mockResolvedValue({ data: { nginx_tenant_directives: "add_header X-A a;" } });
});

describe("DomainAdvancedDirectivesPanel", () => {
  it("loads the domain's current directives into the textarea", async () => {
    renderPanel();
    expect(await screen.findByDisplayValue("add_header X-A a;")).toBeInTheDocument();
    expect(mockGet).toHaveBeenCalledWith("/domains/d1");
  });

  it("saves via PATCH /domains/:id with nginx_tenant_directives", async () => {
    mockPatch.mockResolvedValue({ data: {} });
    renderPanel();
    const textarea = await screen.findByDisplayValue("add_header X-A a;");
    fireEvent.change(textarea, { target: { value: "expires 30d;" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(mockPatch).toHaveBeenCalledWith("/domains/d1", { nginx_tenant_directives: "expires 30d;" }),
    );
  });

  it("surfaces the API's rejection detail (not a generic toast)", async () => {
    mockPatch.mockRejectedValue({
      response: { data: { detail: "advanced directives: 'proxy_pass' is not allowed (only add_header, expires, etag)" } },
    });
    renderPanel();
    const textarea = await screen.findByDisplayValue("add_header X-A a;");
    fireEvent.change(textarea, { target: { value: "proxy_pass http://127.0.0.1:9000;" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(errorToast).toHaveBeenCalledWith(
        "advanced directives: 'proxy_pass' is not allowed (only add_header, expires, etag)",
      ),
    );
  });
});
