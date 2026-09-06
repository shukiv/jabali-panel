// useTransitionalPoll.test.tsx — AC1: one polling rule. The predicate lives in
// utils/applicationStatus (tested there); this proves the *loop* — that a
// transitional row keeps the list refetching on the shared cadence and a fully
// settled list does not, and that landing the last transitional row tears the
// timer down.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook } from "@testing-library/react";
import type { ApplicationStatus } from "../../utils/applicationStatus";
import { useTransitionalPoll, APPLICATION_POLL_MS } from "./useTransitionalPoll";

type Rows = { status: ApplicationStatus }[];

describe("useTransitionalPoll", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("refetches on the poll cadence while a row is transitional", () => {
    const refetch = vi.fn();
    renderHook(() => useTransitionalPoll([{ status: "installing" }], refetch));
    expect(refetch).not.toHaveBeenCalled();
    vi.advanceTimersByTime(APPLICATION_POLL_MS);
    expect(refetch).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(APPLICATION_POLL_MS);
    expect(refetch).toHaveBeenCalledTimes(2);
  });

  it("does not poll when every row has settled", () => {
    const refetch = vi.fn();
    renderHook(() =>
      useTransitionalPoll([{ status: "ready" }, { status: "failed" }], refetch),
    );
    vi.advanceTimersByTime(APPLICATION_POLL_MS * 3);
    expect(refetch).not.toHaveBeenCalled();
  });

  it("stops polling once the last transitional row lands", () => {
    const refetch = vi.fn();
    const { rerender } = renderHook(
      ({ rows }: { rows: Rows }) => useTransitionalPoll(rows, refetch),
      { initialProps: { rows: [{ status: "installing" }] as Rows } },
    );
    vi.advanceTimersByTime(APPLICATION_POLL_MS);
    expect(refetch).toHaveBeenCalledTimes(1);
    rerender({ rows: [{ status: "ready" }] as Rows });
    vi.advanceTimersByTime(APPLICATION_POLL_MS * 3);
    expect(refetch).toHaveBeenCalledTimes(1); // interval was cleared
  });
});
