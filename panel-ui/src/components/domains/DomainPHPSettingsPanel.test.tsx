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

  // GH #1543 (johnnyq): with no per-domain override, each dropdown's inherit
  // option shows the real inherited value labelled "(Default)", not a generic
  // "Use pool default".
  it("labels the inherit option with the real pool default value", async () => {
    mocked.get.mockImplementation((url: string) => {
      if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.3"] } });
      if (url === "/domains/d1/php-settings")
        return Promise.resolve({
          data: {
            ...SETTINGS,
            pool_defaults: {
              memory_limit: "256M",
              max_execution_time: "30",
              // GH #1705 edge cases: error_reporting "0" is a real default (the
              // old !v guard dropped it), and a commented-out date.timezone
              // reports "" whose effective zone is UTC.
              error_reporting: "0",
              "date.timezone": "",
            },
          },
        });
      return Promise.resolve({ data: {} });
    });
    renderPanel();
    // The memory-limit select's inherit option now reads "256M (Default)"; the
    // execution-time one appends the unit → "30s (Default)".
    expect(await screen.findByText("256M (Default)")).toBeInTheDocument();
    expect(screen.getByText("30s (Default)")).toBeInTheDocument();
    // GH #1705: error_reporting 0 maps to the "None" preset; an empty
    // date.timezone surfaces PHP's effective UTC fallback.
    expect(screen.getByText("None (Default)")).toBeInTheDocument();
    expect(screen.getByText("UTC (Default)")).toBeInTheDocument();
    // A directive with no resolved default keeps the generic label.
    expect(screen.getAllByText("Use pool default").length).toBeGreaterThan(0);
  });

  // GH #1705: error_reporting maps known bitmasks to a preset name and shows any
  // other bitmask raw; a set date.timezone shows the zone verbatim.
  it("maps error_reporting presets and shows a named timezone default", async () => {
    mocked.get.mockImplementation((url: string) => {
      if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.3"] } });
      if (url === "/domains/d1/php-settings")
        return Promise.resolve({
          data: {
            ...SETTINGS,
            pool_defaults: {
              error_reporting: "22527",
              "date.timezone": "Europe/Berlin",
            },
          },
        });
      return Promise.resolve({ data: {} });
    });
    renderPanel();
    expect(await screen.findByText("Production (Default)")).toBeInTheDocument();
    expect(screen.getByText("Europe/Berlin (Default)")).toBeInTheDocument();
  });

  it("shows an unmapped error_reporting bitmask raw", async () => {
    mocked.get.mockImplementation((url: string) => {
      if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.3"] } });
      if (url === "/domains/d1/php-settings")
        return Promise.resolve({
          data: { ...SETTINGS, pool_defaults: { error_reporting: "24575" } },
        });
      return Promise.resolve({ data: {} });
    });
    renderPanel();
    // PHP 8.4's E_ALL & ~E_DEPRECATED (24575) isn't one of our presets → raw.
    expect(await screen.findByText("24575 (Default)")).toBeInTheDocument();
  });

  // GH #1705 (johnnyq, latest develop): the real API OMITS every field the
  // domain does not override (omitempty), unlike the SETTINGS fixture above.
  // An absent field reached the Select as undefined, so the closed select
  // showed the grey "Use pool default" placeholder, and the "(Default)" label
  // lived only inside the dropdown.
  const OMITTED = {
    php_version: "8.4",
    pool_defaults: { memory_limit: "256M", max_execution_time: "30" },
  };

  // antd v6: a select holding a value marks its content "has-value".
  const selectedTexts = () =>
    Array.from(document.querySelectorAll(".ant-select-content-has-value")).map((e) =>
      e.getAttribute("title"),
    );

  it("shows the real default as the selected value when the API omits the field", async () => {
    mocked.get.mockImplementation((url: string) => {
      if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.4"] } });
      if (url === "/domains/d1/php-settings") return Promise.resolve({ data: OMITTED });
      return Promise.resolve({ data: {} });
    });
    renderPanel();
    await screen.findByText("userphpsettingspage.php_version");
    await vi.waitFor(() => {
      expect(selectedTexts()).toContain("256M (Default)");
      expect(selectedTexts()).toContain("30s (Default)");
    });
  });

  it("names the default in the placeholder after the field is cleared", async () => {
    mocked.get.mockImplementation((url: string) => {
      if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.4"] } });
      if (url === "/domains/d1/php-settings") return Promise.resolve({ data: OMITTED });
      return Promise.resolve({ data: {} });
    });
    renderPanel();
    await vi.waitFor(() => expect(selectedTexts()).toContain("256M (Default)"));
    // The memory-limit select is the first one after the version select.
    const memory = document.querySelectorAll(".ant-select")[1];
    const clear = memory.querySelector(".ant-select-clear");
    expect(clear).not.toBeNull();
    fireEvent.mouseDown(clear as Element);
    await vi.waitFor(() => {
      expect(memory.querySelector(".ant-select-content-has-value")).toBeNull();
      expect(memory.textContent).toContain("256M (Default)");
    });
  });
});

// GH #1701: the API says which directives the caller may set (`editable`) and
// the owner's package policy. A directive the caller may not set renders
// read-only with a lock tag; an admin sees which ones the tenant cannot change.
describe("DomainPHPSettingsPanel package policy (GH #1701)", () => {
  const ALL = [
    "memory_limit",
    "upload_max_filesize",
    "post_max_size",
    "max_input_vars",
    "max_execution_time",
    "max_input_time",
    "display_errors",
    "error_reporting",
    "date.timezone",
  ];
  const policy = Object.fromEntries(ALL.map((d) => [d, d === "memory_limit" ? "admin_only" : "tenant_allowed"]));

  function withSettings(extra: Record<string, unknown>) {
    mocked.get.mockImplementation((url: string) => {
      if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.3"] } });
      if (url === "/domains/d1/php-settings")
        return Promise.resolve({ data: { ...SETTINGS, php_memory_limit: "256M", policy, ...extra } });
      return Promise.resolve({ data: {} });
    });
  }

  function memoryLimitSelect(): HTMLElement {
    const item = screen.getByText("userphpsettingspage.memory_limit").closest(".ant-form-item");
    const sel = item?.querySelector(".ant-select");
    if (!sel) throw new Error("memory_limit select not found");
    return sel as HTMLElement;
  }

  it("a tenant sees a locked directive read-only, the others editable", async () => {
    withSettings({ editable: ALL.filter((d) => d !== "memory_limit") });
    renderPanel();
    expect(await screen.findByText("Set by your administrator")).toBeInTheDocument();
    expect(screen.getAllByText("Set by your administrator")).toHaveLength(1);
    expect(memoryLimitSelect().className).toContain("ant-select-disabled");
    const upload = screen
      .getByText("userphpsettingspage.upload_max_file_size")
      .closest(".ant-form-item")
      ?.querySelector(".ant-select");
    expect(upload?.className).not.toContain("ant-select-disabled");
  });

  it("an admin can change every directive and sees which ones the tenant cannot", async () => {
    withSettings({ editable: ALL });
    renderPanel();
    expect(await screen.findByText("Admin only")).toBeInTheDocument();
    expect(screen.queryByText("Set by your administrator")).toBeNull();
    expect(memoryLimitSelect().className).not.toContain("ant-select-disabled");
  });
});
