// buildDomainLogColumns.test.tsx — neutral Module tests for the scope routing
// baked into the shared Domain + Actions columns (JAB-296).
//
// We read the RowActions `actions` array straight out of the Actions column's
// render element rather than driving antd's overflow dropdown in happy-dom:
// only the first action renders as a visible button, the rest hide behind a
// click-menu whose motion never settles under happy-dom. Inspecting the
// element's props exercises every log type's onClick without a flaky DOM.
import { describe, it, expect, vi } from "vitest";
import type { ReactElement } from "react";
import { buildDomainLogColumns } from "./buildDomainLogColumns";
import { ALL_DOMAINS_ROW, type DomainLogRow } from "./domainLogStreams";

type RenderCol = {
  key?: string;
  render?: (value: unknown, record: DomainLogRow, index: number) => ReactElement;
};
type WiredAction = { key: string; label: string; onClick?: () => void };

function actionsFor(
  ctx: Parameters<typeof buildDomainLogColumns>[0],
  record: DomainLogRow,
): WiredAction[] {
  const columns = buildDomainLogColumns(ctx) as RenderCol[];
  const actionsCol = columns.find((c) => c.key === "actions");
  const el = actionsCol!.render!(undefined, record, 0);
  return (el.props as { actions: WiredAction[] }).actions;
}

const realRow: DomainLogRow = { id: "d1", name: "example.com", status: "active" };

describe("buildDomainLogColumns — shape (AC6)", () => {
  it("wires the three log types with their labels", () => {
    const actions = actionsFor({ onOpenDomain: vi.fn() }, realRow);
    expect(actions.map((a) => a.key)).toEqual(["access", "error", "goaccess"]);
    expect(actions.map((a) => a.label)).toEqual(["Access Log", "Error Log", "Real Time"]);
  });
});

describe("admin scope — can open aggregate (AC1)", () => {
  it("routes the aggregate row to onOpenAggregate, with no domain identity", () => {
    const onOpenDomain = vi.fn();
    const onOpenAggregate = vi.fn();
    const actions = actionsFor({ onOpenDomain, onOpenAggregate }, ALL_DOMAINS_ROW);
    actions.forEach((a) => a.onClick?.());
    expect(onOpenAggregate.mock.calls).toEqual([["access"], ["error"], ["goaccess"]]);
    expect(onOpenDomain).not.toHaveBeenCalled();
  });

  it("routes a real row to onOpenDomain with its id", () => {
    const onOpenDomain = vi.fn();
    const onOpenAggregate = vi.fn();
    const actions = actionsFor({ onOpenDomain, onOpenAggregate }, realRow);
    actions.forEach((a) => a.onClick?.());
    expect(onOpenDomain.mock.calls).toEqual([
      ["access", "d1"],
      ["error", "d1"],
      ["goaccess", "d1"],
    ]);
    expect(onOpenAggregate).not.toHaveBeenCalled();
  });
});

describe("tenant scope — can NEVER open aggregate (AC2)", () => {
  it("has no aggregate capability, so even the aggregate row routes to onOpenDomain", () => {
    // The tenant ctx has no onOpenAggregate member. Even if an aggregate row
    // somehow reached this scope, the builder cannot emit an aggregate open —
    // the capability is absent, not merely unused.
    const onOpenDomain = vi.fn();
    const actions = actionsFor({ onOpenDomain }, ALL_DOMAINS_ROW);
    actions.forEach((a) => a.onClick?.());
    // Called with the row id ("") — a per-domain open, which the server then
    // rejects for a tenant. It is never an identity-omitting aggregate request.
    expect(onOpenDomain.mock.calls).toEqual([
      ["access", ""],
      ["error", ""],
      ["goaccess", ""],
    ]);
  });

  it("routes a real row to onOpenDomain with its id", () => {
    const onOpenDomain = vi.fn();
    const actions = actionsFor({ onOpenDomain }, realRow);
    actions.forEach((a) => a.onClick?.());
    expect(onOpenDomain.mock.calls).toEqual([
      ["access", "d1"],
      ["error", "d1"],
      ["goaccess", "d1"],
    ]);
  });
});
