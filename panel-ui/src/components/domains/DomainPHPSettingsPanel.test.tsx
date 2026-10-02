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
// Stub the stream modal (it would open a WebSocket); render a marker when shown.
vi.mock("../LogStreamModal", () => ({
  LogStreamModal: (p: { visible: boolean; title: string }) =>
    p.visible ? <div data-testid="log-stream-modal">{p.title}</div> : null,
}));

import { apiClient } from "../../apiClient";
import { DomainPHPSettingsPanel } from "./DomainPHPSettingsPanel";
import {
  PHP_SENSITIVE_DOMAIN_DIRECTIVES,
  PHP_SETTING_DIRECTIVES,
} from "../packages/phpSettingsPolicy";
import { feedback } from "../../lib/feedback";

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

function renderPanel(onDirtyChange?: (dirty: boolean) => void) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <DomainPHPSettingsPanel domainId="d1" onDirtyChange={onDirtyChange} />
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
  // Every directive the page renders, from the shared catalog, so a directive
  // added there is covered here too.
  const ALL: string[] = [...PHP_SETTING_DIRECTIVES, ...PHP_SENSITIVE_DOMAIN_DIRECTIVES];
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

describe("DomainPHPSettingsPanel Reset OPcache (GH #1701)", () => {
  function withReset(allowed: boolean | undefined) {
    mocked.get.mockImplementation((url: string) => {
      if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.3", "8.4"] } });
      if (url === "/domains/d1/php-settings")
        return Promise.resolve({ data: { ...SETTINGS, opcache_reset_allowed: allowed } });
      return Promise.resolve({ data: {} });
    });
  }

  it("resets this domain's OPcache after the confirm", async () => {
    withReset(true);
    mocked.post.mockResolvedValue({ data: { restarted: true, php_version: "8.3" } });
    renderPanel();
    fireEvent.click(await screen.findByText("Reset OPcache"));
    // The confirm names what restarts before anything happens.
    expect(await screen.findByText(/restarts PHP 8\.3 for every site/)).toBeInTheDocument();
    expect(mocked.post).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Reset" }));
    await vi.waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/domains/d1/php-settings/opcache-reset"),
    );
  });

  it("hides the reset when the caller may not reset", async () => {
    withReset(false);
    renderPanel();
    await screen.findByText("View error log");
    expect(screen.queryByText("Reset OPcache")).toBeNull();
  });

  it("hides the reset on an API that does not say", async () => {
    withReset(undefined);
    renderPanel();
    await screen.findByText("View error log");
    expect(screen.queryByText("Reset OPcache")).toBeNull();
  });
});

describe("DomainPHPSettingsPanel error log shortcut (GH #1701)", () => {
  it("opens this domain's error log right on the page", async () => {
    mocked.post.mockResolvedValue({ data: { stream_key: "k1", websocket_url: "/ws/logs/k1" } });
    renderPanel();
    expect(screen.queryByTestId("log-stream-modal")).toBeNull();
    fireEvent.click(await screen.findByText("View error log"));
    await vi.waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/logs/access", { log_type: "error", domain_id: "d1" }),
    );
    expect(await screen.findByTestId("log-stream-modal")).toHaveTextContent("Error Log Stream");
  });
});

// GH #1701 Slice 2: log_errors / file_uploads / short_open_tag.
describe("DomainPHPSettingsPanel flags (GH #1701 Slice 2)", () => {
  function withSettings(extra: Record<string, unknown>) {
    mocked.get.mockImplementation((url: string) => {
      if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.3"] } });
      if (url === "/domains/d1/php-settings")
        return Promise.resolve({ data: { ...SETTINGS, ...extra } });
      return Promise.resolve({ data: {} });
    });
  }

  function flagItem(label: string): HTMLElement {
    const item = screen.getByText(label).closest(".ant-form-item");
    if (!item) throw new Error(`${label} form item not found`);
    return item as HTMLElement;
  }

  it("labels each flag's inherit option with the value it inherits", async () => {
    withSettings({ pool_defaults: { log_errors: "1", file_uploads: "", short_open_tag: "0" } });
    renderPanel();
    await screen.findByText("Log errors");
    // Inheriting (no override): the select shows the inherit option's label.
    expect(flagItem("Log errors").textContent).toContain("On (Default)");
    expect(flagItem("File uploads").textContent).toContain("Off (Default)");
    expect(flagItem("Short open tag").textContent).toContain("Off (Default)");
  });

  it("a locked flag is read-only; a permitted one stays editable", async () => {
    const editable = PHP_SETTING_DIRECTIVES.filter((d) => d !== "short_open_tag");
    withSettings({
      php_short_open_tag: true,
      policy: { short_open_tag: "admin_only" },
      editable,
    });
    renderPanel();
    await screen.findByText("Short open tag");
    const shortSel = flagItem("Short open tag").querySelector(".ant-select");
    expect(shortSel?.className).toContain("ant-select-disabled");
    expect(flagItem("Short open tag").textContent).toContain("Set by your administrator");
    const uploadsSel = flagItem("File uploads").querySelector(".ant-select");
    expect(uploadsSel?.className).not.toContain("ant-select-disabled");
  });
});

describe("DomainPHPSettingsPanel security settings (GH #1701 Slice 3)", () => {
  const ALL: string[] = [...PHP_SETTING_DIRECTIVES, ...PHP_SENSITIVE_DOMAIN_DIRECTIVES];

  function withSettings(extra: Record<string, unknown>) {
    mocked.get.mockImplementation((url: string) => {
      if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.3"] } });
      if (url === "/domains/d1/php-settings")
        return Promise.resolve({ data: { ...SETTINGS, ...extra } });
      return Promise.resolve({ data: {} });
    });
    mocked.patch.mockResolvedValue({});
  }

  function item(label: string): HTMLElement {
    const el = screen.getByText(label).closest(".ant-form-item");
    if (!el) throw new Error(`${label} form item not found`);
    return el as HTMLElement;
  }

  const OPEN_BASEDIR = "Allowed folders (open_basedir)";
  const ALLOW_URL_FOPEN = "Remote file access (allow_url_fopen)";

  it("shows the stored open_basedir and the inherited allow_url_fopen", async () => {
    withSettings({ php_open_basedir: "{DOCROOT}:{TMP}", pool_defaults: { allow_url_fopen: "1" }, editable: ALL });
    renderPanel();
    await screen.findByText(OPEN_BASEDIR);
    const input = item(OPEN_BASEDIR).querySelector("input") as HTMLInputElement;
    expect(input.value).toBe("{DOCROOT}:{TMP}");
    expect(item(ALLOW_URL_FOPEN).textContent).toContain("On (Default)");
  });

  it("saves a typed open_basedir trimmed, and an emptied one as inherit", async () => {
    const { waitFor } = await import("@testing-library/react");
    withSettings({ php_open_basedir: "{DOCROOT}", editable: ALL });
    renderPanel();
    await screen.findByText(OPEN_BASEDIR);
    const input = item(OPEN_BASEDIR).querySelector("input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: " {DOCROOT}:/home/u1/lib " } });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalledTimes(1));
    expect(mocked.patch.mock.calls[0][1]).toMatchObject({
      php_open_basedir: "{DOCROOT}:/home/u1/lib",
      php_allow_url_fopen: null,
    });

    // The save reloads the settings and reseeds the form (here the mock still
    // returns {DOCROOT}), which remounts the input: look it up again.
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("No unsaved changes"));
    const reseeded = item(OPEN_BASEDIR).querySelector("input") as HTMLInputElement;
    expect(reseeded.value).toBe("{DOCROOT}");
    fireEvent.change(reseeded, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalledTimes(2));
    expect(mocked.patch.mock.calls[1][1]).toMatchObject({ php_open_basedir: null });
  });

  it("a tenant without the opt-in sees both read-only and sends them back as stored", async () => {
    const { waitFor } = await import("@testing-library/react");
    withSettings({
      php_open_basedir: "{DOCROOT}:/usr/share/php",
      php_allow_url_fopen: false,
      php_timezone: "UTC",
      policy: { open_basedir: "admin_only", allow_url_fopen: "admin_only" },
      editable: [...PHP_SETTING_DIRECTIVES],
    });
    renderPanel();
    await screen.findByText(OPEN_BASEDIR);
    expect((item(OPEN_BASEDIR).querySelector("input") as HTMLInputElement).disabled).toBe(true);
    expect(item(ALLOW_URL_FOPEN).querySelector(".ant-select")?.className).toContain("ant-select-disabled");
    expect(item(OPEN_BASEDIR).textContent).toContain("Set by your administrator");

    // A permitted change: clear the stored timezone.
    const clear = item("Timezone").querySelector(".ant-select-clear");
    expect(clear).not.toBeNull();
    fireEvent.mouseDown(clear as Element);
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalledTimes(1));
    expect(mocked.patch.mock.calls[0][1]).toMatchObject({
      php_timezone: null,
      php_open_basedir: "{DOCROOT}:/usr/share/php",
      php_allow_url_fopen: false,
    });
  });

  it("shows the server's reason when it refuses a value", async () => {
    const { waitFor } = await import("@testing-library/react");
    withSettings({ editable: ALL });
    mocked.patch.mockRejectedValue({
      response: { data: { error: 'invalid_php_setting: open_basedir: "/srv" is outside your home directory /home/u1' } },
    });
    renderPanel();
    await screen.findByText(OPEN_BASEDIR);
    const input = item(OPEN_BASEDIR).querySelector("input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "/srv" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));
    await waitFor(() =>
      expect(feedback.message.error).toHaveBeenCalledWith(
        'open_basedir: "/srv" is outside your home directory /home/u1',
      ),
    );
  });
});

// GH #1701 (lxsdevcode, 10-01): the page says what each setting runs with,
// which settings are custom and what is not saved yet; it keeps Save in view,
// collapses its sections and asks before unsaved changes are lost.
describe("DomainPHPSettingsPanel page UX (GH #1701)", () => {
  const ALL: string[] = [...PHP_SETTING_DIRECTIVES, ...PHP_SENSITIVE_DOMAIN_DIRECTIVES];
  const OPEN_BASEDIR = "Allowed folders (open_basedir)";

  function withSettings(extra: Record<string, unknown>) {
    mocked.get.mockImplementation((url: string) => {
      if (url === "/php/versions") return Promise.resolve({ data: { versions: ["8.3"] } });
      if (url === "/domains/d1/php-settings")
        return Promise.resolve({ data: { ...SETTINGS, editable: ALL, ...extra } });
      return Promise.resolve({ data: {} });
    });
    mocked.patch.mockResolvedValue({});
  }

  function item(label: string): HTMLElement {
    const el = screen.getByText(label).closest(".ant-form-item");
    if (!el) throw new Error(`${label} form item not found`);
    return el as HTMLElement;
  }
  const basedirInput = () => item(OPEN_BASEDIR).querySelector("input") as HTMLInputElement;
  const status = () => screen.getByRole("status").textContent;
  const saveButton = () => screen.getByRole("button", { name: "Save Changes" }) as HTMLButtonElement;
  function sectionHeader(label: string): HTMLElement {
    const el = screen.getByText(label).closest(".ant-collapse-header");
    if (!el) throw new Error(`${label} section header not found`);
    return el as HTMLElement;
  }

  it("shows the value display_errors inherits, not a bare 'Use pool default'", async () => {
    withSettings({});
    renderPanel();
    await screen.findByText(OPEN_BASEDIR);
    const select = document.getElementById("php_display_errors")?.closest(".ant-select");
    expect(select?.textContent).toContain("Off (Default)");
  });

  it("keeps a collapsed section's settings in the form, and opens a section that holds a custom value", async () => {
    withSettings({});
    const { unmount } = renderPanel();
    await screen.findByText(OPEN_BASEDIR);
    // Security holds nothing custom: collapsed, but its fields are rendered so
    // a save never reads them as cleared.
    expect(sectionHeader("Security").getAttribute("aria-expanded")).toBe("false");
    expect(document.getElementById("php_open_basedir")).not.toBeNull();
    expect(document.getElementById("php_allow_url_fopen")).not.toBeNull();
    unmount();

    withSettings({ php_allow_url_fopen: false });
    renderPanel();
    await screen.findByText(OPEN_BASEDIR);
    expect(sectionHeader("Security").getAttribute("aria-expanded")).toBe("true");
    expect(sectionHeader("Security").textContent).toContain("1 custom");
  });

  it("counts a change as unsaved only while it differs from the stored value", async () => {
    withSettings({ php_open_basedir: "{DOCROOT}" });
    renderPanel();
    await screen.findByText(OPEN_BASEDIR);
    expect(status()).toBe("No unsaved changes");
    expect(saveButton().disabled).toBe(true);

    fireEvent.change(basedirInput(), { target: { value: "{DOCROOT}:{TMP}" } });
    expect(status()).toBe("Unsaved changes (1)");
    expect(saveButton().disabled).toBe(false);
    expect(item(OPEN_BASEDIR).textContent).toContain("Unsaved");
    expect(sectionHeader("Security").textContent).toContain("1 unsaved");

    // Back to the stored value (spaces are trimmed on save): nothing to save.
    fireEvent.change(basedirInput(), { target: { value: " {DOCROOT} " } });
    expect(status()).toBe("No unsaved changes");
    expect(saveButton().disabled).toBe(true);
  });

  it("Reset to default clears a custom value, and the save sends it as inherit", async () => {
    const { waitFor } = await import("@testing-library/react");
    withSettings({ php_memory_limit: "1G" });
    renderPanel();
    await screen.findByText("userphpsettingspage.memory_limit");
    const memory = () => item("userphpsettingspage.memory_limit");
    expect(memory().textContent).toContain("Custom");

    fireEvent.click(screen.getByRole("button", { name: "Reset userphpsettingspage.memory_limit to default" }));
    expect(memory().textContent).toContain("Pool default");
    expect(memory().textContent).toContain("Unsaved");
    expect(screen.queryByRole("button", { name: "Reset userphpsettingspage.memory_limit to default" })).toBeNull();
    expect(status()).toBe("Unsaved changes (1)");

    fireEvent.click(saveButton());
    await waitFor(() => expect(mocked.patch).toHaveBeenCalledTimes(1));
    expect(mocked.patch.mock.calls[0][1]).toMatchObject({ php_memory_limit: null });
  });

  it("offers no reset on a setting the caller may not change", async () => {
    withSettings({
      php_memory_limit: "1G",
      editable: ALL.filter((d) => d !== "memory_limit"),
    });
    renderPanel();
    await screen.findByText("Set by your administrator");
    expect(item("userphpsettingspage.memory_limit").textContent).toContain("Custom");
    expect(screen.queryByRole("button", { name: /^Reset userphpsettingspage.memory_limit/ })).toBeNull();
  });

  it("Discard puts the stored values back", async () => {
    withSettings({ php_open_basedir: "{DOCROOT}" });
    renderPanel();
    await screen.findByText(OPEN_BASEDIR);
    fireEvent.change(basedirInput(), { target: { value: "/home/u1/x" } });
    expect(status()).toBe("Unsaved changes (1)");

    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(status()).toBe("No unsaved changes");
    expect(basedirInput().value).toBe("{DOCROOT}");
    expect(screen.queryByRole("button", { name: "Discard" })).toBeNull();
  });

  it("tells the host about unsaved changes, and the browser asks before leaving with them", async () => {
    withSettings({ php_open_basedir: "{DOCROOT}" });
    const onDirty = vi.fn();
    const { unmount } = renderPanel(onDirty);
    await screen.findByText(OPEN_BASEDIR);
    const leave = () => {
      const e = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(e);
      return e.defaultPrevented;
    };
    expect(leave()).toBe(false);

    fireEvent.change(basedirInput(), { target: { value: "/home/u1/x" } });
    expect(onDirty).toHaveBeenLastCalledWith(true);
    expect(leave()).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(onDirty).toHaveBeenLastCalledWith(false);
    expect(leave()).toBe(false);

    // Unmounted with unsaved changes: the host hears it holds none any more.
    fireEvent.change(basedirInput(), { target: { value: "/home/u1/y" } });
    expect(onDirty).toHaveBeenLastCalledWith(true);
    unmount();
    expect(onDirty).toHaveBeenLastCalledWith(false);
    expect(leave()).toBe(false);
  });
});
