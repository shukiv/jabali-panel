// DisksTable — GH #2029: a disk's percent is df's Use%, used / (used + free)
// rounded up, so blocks ext4 reserves for root count as neither used nor free.
// The 80% / 95% marks apply to that same number.
import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { DisksTable } from "./DisksTable";
import type { Partition } from "../../../hooks/useServerStatus";

function rowOf(mount: string): HTMLElement {
  const row = screen.getAllByRole("row").find((r) => within(r).queryByText(mount) !== null);
  if (!row) throw new Error(`no row for ${mount}`);
  return row;
}

describe("DisksTable", () => {
  it("shows the reporter's disk as df does: 5%, healthy", () => {
    const p: Partition = {
      mount_point: "/",
      total_bytes: 760740884480,
      used_bytes: 32386052096,
      free_bytes: 697378873344,
    };
    render(<DisksTable partitions={[p]} />);
    const row = rowOf("/");
    expect(within(row).getByText("5%")).toBeInTheDocument();
    expect(within(row).getByText("healthy")).toBeInTheDocument();
  });

  it("measures the 80% and 95% marks against the space non-root can use", () => {
    render(
      <DisksTable
        partitions={[
          { mount_point: "/var", total_bytes: 100_000, used_bytes: 76_000, free_bytes: 19_000 },
          { mount_point: "/home", total_bytes: 100_000, used_bytes: 90_500, free_bytes: 4_500 },
        ]}
      />,
    );
    expect(within(rowOf("/var")).getByText("80%")).toBeInTheDocument();
    expect(within(rowOf("/var")).getByText("warning")).toBeInTheDocument();
    expect(within(rowOf("/home")).getByText("96%")).toBeInTheDocument();
    expect(within(rowOf("/home")).getByText("critical")).toBeInTheDocument();
  });

  it("shows 0% for a mount with nothing used and nothing free to non-root", () => {
    render(<DisksTable partitions={[{ mount_point: "/boot", total_bytes: 1000, used_bytes: 0, free_bytes: 0 }]} />);
    expect(within(rowOf("/boot")).getByText("0%")).toBeInTheDocument();
  });
});
