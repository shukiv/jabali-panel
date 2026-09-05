// ApplicationDomainCell.test.tsx — the domain/CMS cell both lists render. A
// ready install links to its live site (honoring the subdirectory); anything
// else is plain text; a missing domain name falls back to the domain id. The
// optional status slot stacks under the link for the tenant list and is absent
// for the admin list (which keeps status in its own column). CmsIcon reads the
// theme, so renders are wrapped in ThemeModeProvider.
import { describe, it, expect } from "vitest";
import { type ReactElement } from "react";
import { render, screen } from "@testing-library/react";
import { ThemeModeProvider } from "../../theme/ThemeModeContext";
import { ApplicationDomainCell } from "./ApplicationDomainCell";
import { ApplicationStatusTag } from "./ApplicationStatusTag";
import type { ApplicationInstall } from "./applicationInventory";

const renderCell = (ui: ReactElement) =>
  render(<ThemeModeProvider>{ui}</ThemeModeProvider>);

const row = (over: Partial<ApplicationInstall> = {}): ApplicationInstall =>
  ({
    id: "i1",
    domain_name: "site.test",
    domain_id: "d1",
    subdirectory: "",
    status: "ready",
    app_type: "wordpress",
    ...over,
  }) as ApplicationInstall;

describe("ApplicationDomainCell", () => {
  it("links a ready install to its live site, honoring the subdirectory", () => {
    renderCell(<ApplicationDomainCell record={row({ subdirectory: "blog" })} />);
    const link = screen.getByRole("link", { name: "site.test/blog/" });
    expect(link).toHaveAttribute("href", "https://site.test/blog/");
  });

  it("renders plain text (no link) for a non-ready install", () => {
    renderCell(<ApplicationDomainCell record={row({ status: "installing" })} />);
    expect(screen.getByText("site.test/")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("falls back to the domain id when there is no domain name", () => {
    renderCell(<ApplicationDomainCell record={row({ domain_name: "" })} />);
    expect(screen.getByText("d1/")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("renders the status slot when supplied (tenant list)", () => {
    renderCell(
      <ApplicationDomainCell
        record={row({ status: "failed" })}
        status={<ApplicationStatusTag status="failed" />}
      />,
    );
    expect(screen.getByText("Failed")).toBeInTheDocument();
  });

  it("omits the status slot when none is supplied (admin list)", () => {
    renderCell(<ApplicationDomainCell record={row({ status: "failed" })} />);
    expect(screen.queryByText("Failed")).not.toBeInTheDocument();
  });
});
