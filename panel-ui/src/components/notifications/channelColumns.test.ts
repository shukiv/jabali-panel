// channelColumns.test — the owner column is admin-only (AC4). The builder must
// include the user_id column only when showOwnerColumn is set, so a tenant (who
// only ever sees its own rows) never renders it.
import { describe, expect, it } from "vitest";

import { buildChannelColumns } from "./channelColumns";

const labels = { name: "Name", kind: "Kind", owner: "Owner", enabled: "Enabled", actions: "Actions" };

function dataIndexes(cols: ReturnType<typeof buildChannelColumns>): (string | undefined)[] {
  return cols.map((c) => (c as { dataIndex?: string }).dataIndex);
}

describe("buildChannelColumns owner column (AC4)", () => {
  const base = {
    labels,
    onOpenEdit: () => {},
    onToggleEnabled: () => {},
    renderActions: () => null,
  };

  it("includes the owner column when showOwnerColumn is true (admin)", () => {
    const cols = buildChannelColumns({ ...base, showOwnerColumn: true });
    expect(dataIndexes(cols)).toContain("user_id");
  });

  it("omits the owner column when showOwnerColumn is false (tenant)", () => {
    const cols = buildChannelColumns({ ...base, showOwnerColumn: false });
    expect(dataIndexes(cols)).not.toContain("user_id");
  });
});
