// GH #2242: alerts took most of the screen on mobile (the Updates page opened
// on three of them), and an alert closed with its X came back on the next
// load. CollapsibleAlert folds an alert to its title and remembers, per
// browser, whether it was opened, folded or closed.
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { CollapsibleAlert } from "./CollapsibleAlert";

const KEY = "jabali.alert.test.note";

const note = (props: Partial<Parameters<typeof CollapsibleAlert>[0]> = {}) => (
  <CollapsibleAlert
    storageKey="test.note"
    type="info"
    showIcon
    title="Short title"
    description="The long explanation"
    {...props}
  />
);

describe("CollapsibleAlert (GH #2242)", () => {
  beforeEach(() => {
    window.localStorage.clear();
    vi.restoreAllMocks();
  });

  it("shows only the title of a plain note until the arrow is clicked", () => {
    render(note());
    expect(screen.getByText("Short title")).toBeInTheDocument();
    expect(screen.queryByText("The long explanation")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Show details" }));
    expect(screen.getByText("The long explanation")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Hide details" })).toHaveAttribute("aria-expanded", "true");
  });

  // The reporter's follow-up on #2242: an arrow only, no "Details" / "Hide"
  // text taking room beside the title. Screen readers still get a name.
  it("shows the toggle as an arrow with no text", () => {
    render(note());
    const toggle = screen.getByRole("button", { name: "Show details" });
    expect(toggle.textContent).toBe("");
    expect(toggle.querySelector("svg")).not.toBeNull();
    expect(toggle).toHaveAttribute("title", "Show details");
  });

  it("opens a safety warning on the first visit", () => {
    render(note({ defaultOpen: true }));
    expect(screen.getByText("The long explanation")).toBeInTheDocument();
  });

  it("keeps a folded warning folded on the next load", () => {
    const first = render(note({ defaultOpen: true }));
    fireEvent.click(screen.getByRole("button", { name: /hide/i }));
    expect(screen.queryByText("The long explanation")).not.toBeInTheDocument();
    first.unmount();

    render(note({ defaultOpen: true }));
    expect(screen.getByText("Short title")).toBeInTheDocument();
    expect(screen.queryByText("The long explanation")).not.toBeInTheDocument();
    expect(window.localStorage.getItem(KEY)).toBe("folded");
  });

  it("keeps an opened note open on the next load", () => {
    const first = render(note());
    fireEvent.click(screen.getByRole("button", { name: /details/i }));
    first.unmount();

    render(note());
    expect(screen.getByText("The long explanation")).toBeInTheDocument();
  });

  it("keeps a closed alert closed on the next load", () => {
    const first = render(note({ defaultOpen: true, dismissible: true }));
    fireEvent.click(screen.getByRole("button", { name: /close/i }));
    expect(screen.queryByText("Short title")).not.toBeInTheDocument();
    first.unmount();

    const { container } = render(note({ defaultOpen: true, dismissible: true }));
    expect(container).toBeEmptyDOMElement();
  });

  it("keeps the action visible while folded", () => {
    render(note({ action: <button type="button">Update now</button> }));
    expect(screen.queryByText("The long explanation")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update now" })).toBeInTheDocument();
  });

  it("offers no toggle when there is nothing to fold", () => {
    render(note({ description: undefined }));
    expect(screen.queryByRole("button", { name: /details|hide/i })).not.toBeInTheDocument();
  });

  it("uses the new key's state when the key changes", () => {
    window.localStorage.setItem("jabali.alert.test.a", "folded");
    const { rerender } = render(note({ storageKey: "test.a", defaultOpen: true }));
    expect(screen.queryByText("The long explanation")).not.toBeInTheDocument();

    rerender(note({ storageKey: "test.b", defaultOpen: true }));
    expect(screen.getByText("The long explanation")).toBeInTheDocument();
  });

  it("still works when the browser blocks storage", () => {
    const getItem = vi.spyOn(window.localStorage, "getItem").mockImplementation(() => {
      throw new Error("SecurityError");
    });
    const setItem = vi.spyOn(window.localStorage, "setItem").mockImplementation(() => {
      throw new Error("SecurityError");
    });
    render(note({ defaultOpen: true }));
    expect(screen.getByText("The long explanation")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /hide/i }));
    expect(screen.queryByText("The long explanation")).not.toBeInTheDocument();
    expect(getItem).toHaveBeenCalled();
    expect(setItem).toHaveBeenCalled();
  });
});
