// GH #1232 — "Records per page selection is broken across the board."
//
// Root cause: AntD treats `pagination.pageSize` as a CONTROLLED value. A
// client-side table that hardcodes a literal `pageSize: N` re-passes that same
// literal on every render, so when the user picks a new size the page-size
// changer snaps straight back to N. (In v6 the changer auto-appears once total
// > 50 — `totalBoundaryShowSizeChanger` — so any list long enough to page is
// affected, whether or not `showSizeChanger` is set.)
//
// The fix for client-side tables (full dataSource, no server paging) is to seed
// the size with the UNCONTROLLED `defaultPageSize` and let AntD own it. These
// tests pin both halves so the controlled-literal pattern can't creep back in.
import { render, screen, fireEvent, within, cleanup } from "@testing-library/react";
import { describe, it, expect, afterEach } from "vitest";
import { Table } from "antd";

afterEach(cleanup);

const rows = Array.from({ length: 60 }, (_, i) => ({ id: i, name: `row-${i}` }));
const columns = [{ title: "Name", dataIndex: "name", key: "name" }];

function countBodyRows(container: HTMLElement): number {
  return container.querySelectorAll("tbody tr.ant-table-row").length;
}

// Open the page-size changer and click the "<size> / page" option.
function pickPageSize(container: HTMLElement, size: number) {
  const selector = container.querySelector(
    ".ant-pagination-options-size-changer .ant-select-content",
  );
  if (!selector) throw new Error("page-size changer not rendered");
  fireEvent.mouseDown(selector);
  // Options render in a portal on document.body.
  const option = screen.getByText(`${size} / page`);
  fireEvent.click(option);
}

describe("GH #1232 — records-per-page selection sticks", () => {
  it("controlled literal pageSize SNAPS BACK — the bug", () => {
    const { container } = render(
      <Table
        rowKey="id"
        dataSource={rows}
        columns={columns}
        pagination={{ pageSize: 25, showSizeChanger: true }}
      />,
    );
    expect(countBodyRows(container)).toBe(25);
    pickPageSize(container, 50);
    // Parent re-passes the same literal 25 → size reverts, still 25 rows.
    expect(countBodyRows(container)).toBe(25);
  });

  it("uncontrolled defaultPageSize STICKS — the fix", () => {
    const { container } = render(
      <Table
        rowKey="id"
        dataSource={rows}
        columns={columns}
        pagination={{ defaultPageSize: 25, showSizeChanger: true }}
      />,
    );
    expect(countBodyRows(container)).toBe(25);
    pickPageSize(container, 50);
    // AntD owns the size now → 50 rows render and stay.
    expect(countBodyRows(container)).toBe(50);
  });

  it("a default size of 25 (absent from AntD's 10/20/50/100 options) stays selectable", () => {
    const { container } = render(
      <Table
        rowKey="id"
        dataSource={rows}
        columns={columns}
        pagination={{ defaultPageSize: 25, showSizeChanger: true }}
      />,
    );
    const selector = container.querySelector(
      ".ant-pagination-options-size-changer .ant-select-content",
    ) as HTMLElement;
    // The changer shows the seeded 25 as its current value…
    expect(within(selector).getByText("25 / page")).toBeInTheDocument();
    fireEvent.mouseDown(selector);
    // …and rc-pagination injects 25 into the option list, so the user can
    // always return to it. If this regresses, the affected tables need an
    // explicit pageSizeOptions that includes 25.
    expect(screen.getByText("50 / page")).toBeInTheDocument();
    expect(screen.getAllByText("25 / page").length).toBeGreaterThanOrEqual(1);
  });
});
