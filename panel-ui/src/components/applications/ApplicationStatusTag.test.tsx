// ApplicationStatusTag.test.tsx — AC2 failure presentation. A failed row with
// a last_error surfaces that error (in a tooltip); a failed row without one is
// a plain tag; every other status is a plain tag. One failure presentation for
// both lists.
import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ApplicationStatusTag } from "./ApplicationStatusTag";

describe("ApplicationStatusTag", () => {
  it("renders the status label", () => {
    render(<ApplicationStatusTag status="ready" />);
    expect(screen.getByText("Ready")).toBeInTheDocument();
  });

  it("surfaces the failure reason on hover for a failed row", async () => {
    render(<ApplicationStatusTag status="failed" lastError="disk full" />);
    fireEvent.mouseEnter(screen.getByText("Failed"));
    expect(await screen.findByText("disk full")).toBeInTheDocument();
  });

  it("renders a plain failed tag when there is no error detail", () => {
    render(<ApplicationStatusTag status="failed" />);
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.queryByText("disk full")).not.toBeInTheDocument();
  });
});
