// DomainApplicationCell.test — GH #1543 (D1). The Application cell shows the
// primary (docroot) install: badge + version for a ready app with a Login, the
// live status instead of a version while it settles, an em dash when empty, and
// a "+N more" link when a domain hosts several installs.
import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("../../hooks/useMagicLink", () => ({
  useMagicLink: () => ({ mint: vi.fn(), loading: false, error: null }),
}));

import { DomainApplicationCell } from "./DomainApplicationCell";
import type { DomainApplicationSummary } from "./types";

const app = (over: Partial<DomainApplicationSummary> = {}): DomainApplicationSummary => ({
  id: "i1",
  app_type: "wordpress",
  version: "6.5.3",
  status: "ready",
  subdirectory: "",
  ...over,
});

const renderCell = (applications?: DomainApplicationSummary[]) =>
  render(
    <MemoryRouter>
      <DomainApplicationCell applications={applications} />
    </MemoryRouter>,
  );

describe("DomainApplicationCell (GH #1543)", () => {
  it("shows an em dash when the domain has no apps", () => {
    renderCell([]);
    expect(screen.getByText("—")).toBeInTheDocument();
  });

  it("shows badge, version and Login for a ready WordPress install", () => {
    renderCell([app()]);
    expect(screen.getByText("WordPress")).toBeInTheDocument();
    expect(screen.getByText("6.5.3")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Login" })).toBeInTheDocument();
  });

  it("shows the live status (not a version or Login) while installing", () => {
    renderCell([app({ status: "installing", version: null })]);
    expect(screen.queryByRole("button", { name: "Login" })).not.toBeInTheDocument();
    // A version string must not stand in for an unsettled install.
    expect(screen.queryByText("6.5.3")).not.toBeInTheDocument();
  });

  it("collapses extra installs into a +N more link", () => {
    renderCell([app(), app({ id: "i2", subdirectory: "blog" })]);
    const link = screen.getByRole("link", { name: "+1 more" });
    expect(link).toHaveAttribute("href", "/jabali-panel/applications");
  });

  // D2: inline mutations, driven by callbacks the tenant grid supplies.
  it("offers Install on an empty domain when onInstall is wired (D2)", () => {
    const onInstall = vi.fn();
    render(
      <MemoryRouter>
        <DomainApplicationCell applications={[]} onInstall={onInstall} />
      </MemoryRouter>,
    );
    expect(screen.queryByText("—")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Install/ }));
    expect(onInstall).toHaveBeenCalledTimes(1);
  });

  it("deletes the primary install when onDelete is wired (D2)", () => {
    const onDelete = vi.fn();
    const primary = app();
    render(
      <MemoryRouter>
        <DomainApplicationCell
          applications={[primary, app({ id: "i2", subdirectory: "blog" })]}
          onDelete={onDelete}
        />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Delete app" }));
    expect(onDelete).toHaveBeenCalledWith(primary);
  });

  it("has no Install or Delete controls without the callbacks (read-only)", () => {
    renderCell([app()]); // no onInstall/onDelete
    expect(screen.queryByRole("button", { name: "Delete app" })).not.toBeInTheDocument();
    // Login still shows for a ready SSO app; Delete must not.
    expect(screen.getByRole("button", { name: "Login" })).toBeInTheDocument();
  });
});
