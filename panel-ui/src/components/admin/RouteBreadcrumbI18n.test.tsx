import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";

import { RouteBreadcrumb } from "./RouteBreadcrumb";
import type { NavItem } from "../../nav";

// GH #455: nav labels are i18n keys ("nav.admin.users"). The auto breadcrumb
// used them verbatim after the multi-language update, showing raw keys. The
// component must run them through t(). Test setup loads the real i18n instance,
// so "nav.admin.users" resolves to "Users".
const nav: NavItem[] = [{ key: "users", label: "nav.admin.users", icon: null, path: "/jabali-admin/users" }];

describe("RouteBreadcrumb i18n (GH #455)", () => {
  it("translates nav key labels instead of rendering the raw key", () => {
    const { queryByText } = render(
      <MemoryRouter initialEntries={["/jabali-admin/users"]}>
        <RouteBreadcrumb nav={nav} homePath="/jabali-admin/dashboard" homeLabel="Dashboard" />
      </MemoryRouter>,
    );
    expect(queryByText("nav.admin.users")).toBeNull(); // no raw key on screen
    expect(queryByText("Users")).not.toBeNull(); // translated label shown
  });
});
