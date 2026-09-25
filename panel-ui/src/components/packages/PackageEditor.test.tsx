// PackageEditor.test.tsx — JAB-331 AC1. Mount the shared Module once and assert
// every canonical entitlement field renders. This is the render-path witness that
// both PackageCreate and PackageEdit go through one form, and it catches a
// byName() lookup returning undefined (which would crash at render) — something
// only Playwright would otherwise see.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

// No network: the two catalog fetches and the disk-quota settings query all land
// on the same apiClient; an empty payload exercises the "|| []" / "?? false" paths.
vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn().mockResolvedValue({ data: {} }) },
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (k: string) => k }) }));

import { PackageEditor } from "./PackageEditor";
import { PACKAGE_DEFAULTS, PACKAGE_LIMIT_FIELDS } from "./packageFields";

function renderEditor() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PackageEditor title="Create package" submitting={false} onSubmit={vi.fn()} />
    </QueryClientProvider>,
  );
}

describe("PackageEditor renders the full entitlement set (JAB-331 AC1)", () => {
  it("renders a labelled field for every canonical limit field", () => {
    renderEditor();
    for (const f of PACKAGE_LIMIT_FIELDS) {
      expect(screen.getByText(`packageedit.${f.labelKey}`), `missing field label: ${f.name}`).toBeTruthy();
    }
  });

  it("renders the special disk-quota field and the name field", () => {
    renderEditor();
    expect(screen.getByText("packageedit.disk_quota_mb")).toBeTruthy();
    expect(screen.getByText("packageedit.name")).toBeTruthy();
  });

  it("renders the passed title and a Save button", () => {
    renderEditor();
    expect(screen.getByText("Create package")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();
  });

  // GH #1628: webmail is a package entitlement that defaults ON. In create mode
  // (no record) the form seeds from PACKAGE_DEFAULTS, so the Webmail switch must
  // render already checked — the admin has to opt OUT, not opt in.
  it("renders the Webmail toggle defaulting ON", () => {
    renderEditor();
    const row = screen.getByText("Webmail Enabled").closest("div");
    expect(row).toBeTruthy();
    const sw = row?.querySelector('[role="switch"]');
    expect(sw, "Webmail switch renders").toBeTruthy();
    expect(sw?.getAttribute("aria-checked")).toBe("true");
  });
});

// GH #1798: outbound SSH and ping for the M34 egress firewall are per-package
// opt-ins that default OFF (the firewall stays closed unless the admin opens it).
describe("PackageEditor egress allowances (GH #1798)", () => {
  it("renders the outbound SSH and ping switches defaulting OFF", () => {
    renderEditor();
    for (const label of ["Allow outbound SSH (port 22)", "Allow ping (ICMP echo)"]) {
      const row = screen.getByText(label).closest("div");
      const sw = row?.querySelector('[role="switch"]');
      expect(sw, `${label} switch renders`).toBeTruthy();
      expect(sw?.getAttribute("aria-checked"), label).toBe("false");
    }
  });

  it("hides the SSH destination list until outbound SSH is on", () => {
    renderEditor();
    expect(screen.queryByText("Outbound SSH destinations")).toBeNull();
  });

  it("loads a stored package's outbound SSH scope into the destination list (edit mode)", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <PackageEditor
          title="Edit package"
          initialValue={{
            ...PACKAGE_DEFAULTS,
            id: "pkg-1",
            egress_ssh_out: true,
            egress_ssh_out_cidrs: '["203.0.113.0/24"]',
            egress_icmp: true,
          }}
          submitting={false}
          onSubmit={vi.fn()}
        />
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Outbound SSH destinations")).toBeTruthy();
    expect(await screen.findByText("203.0.113.0/24")).toBeTruthy();
    const ping = screen.getByText("Allow ping (ICMP echo)").closest("div")?.querySelector('[role="switch"]');
    expect(ping?.getAttribute("aria-checked")).toBe("true");
  });
});
