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
import { PACKAGE_LIMIT_FIELDS } from "./packageFields";

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
});
