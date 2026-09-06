// DomainPHPSettingsPanel.test — GH #1543 / GH #1332. The per-domain PHP panel
// binds every request to the domain it is given. This pins that at the extracted
// call site: it loads THIS domain's settings on mount, and changing the version
// to "Server default" DELETEs THIS domain's pool (never a stale/other domain).
import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn(), patch: vi.fn() },
}));
vi.mock("../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

import { apiClient } from "../../apiClient";
import { DomainPHPSettingsPanel } from "./DomainPHPSettingsPanel";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
};

const SETTINGS = {
  php_version: "8.3",
  php_memory_limit: null,
  php_upload_max_filesize: null,
  php_post_max_size: null,
  php_max_input_vars: null,
  php_max_execution_time: null,
  php_max_input_time: null,
  php_display_errors: null,
  php_error_reporting: null,
  php_timezone: null,
};

beforeEach(() => {
  vi.clearAllMocks();
  mocked.get.mockImplementation((url: string) => {
    if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.3", "8.4"] } });
    if (url === "/domains/d1/php-settings") return Promise.resolve({ data: SETTINGS });
    return Promise.resolve({ data: {} });
  });
  mocked.delete.mockResolvedValue({});
  mocked.post.mockResolvedValue({});
});

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <DomainPHPSettingsPanel domainId="d1" />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("DomainPHPSettingsPanel (GH #1543)", () => {
  it("loads THIS domain's PHP settings on mount", async () => {
    renderPanel();
    await vi.waitFor(() =>
      expect(mocked.get).toHaveBeenCalledWith("/domains/d1/php-settings"),
    );
  });

  it("reverting the version to Server default DELETEs this domain's pool", async () => {
    renderPanel();
    // Wait for the form to appear (phpSettings loaded).
    await screen.findByText("userphpsettingspage.php_version");
    // The version Select is the first combobox; open it and pick Server default.
    fireEvent.mouseDown(screen.getAllByRole("combobox")[0]);
    fireEvent.click(await screen.findByText("Server default"));
    await vi.waitFor(() =>
      expect(mocked.delete).toHaveBeenCalledWith("/domains/d1/php-pool"),
    );
  });
});
