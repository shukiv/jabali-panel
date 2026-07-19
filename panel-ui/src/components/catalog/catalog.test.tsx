// JAB-167: lock the behavior of the shared marketplace catalog primitives so a
// refactor of any one page can't quietly break the others.
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { CatalogCard } from "./CatalogCard";
import { CategoryFilter } from "./CategoryFilter";

describe("CatalogCard", () => {
  it("renders name, meta, description and fires onInstall", () => {
    const onInstall = vi.fn();
    render(
      <CatalogCard
        name="Django"
        meta="WSGI"
        description="Batteries-included web framework."
        onInstall={onInstall}
      />,
    );
    expect(screen.getByText("Django")).toBeTruthy();
    expect(screen.getByText("WSGI")).toBeTruthy();
    expect(screen.getByText("Batteries-included web framework.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Install" }));
    expect(onInstall).toHaveBeenCalledOnce();
  });

  it("honors a custom install label + disabled", () => {
    const onInstall = vi.fn();
    render(<CatalogCard name="X" installLabel="Deploy" disabled onInstall={onInstall} />);
    const btn = screen.getByRole("button", { name: "Deploy" });
    fireEvent.click(btn);
    expect(onInstall).not.toHaveBeenCalled();
  });
});

describe("CategoryFilter", () => {
  it("renders nothing with no tags", () => {
    const { container } = render(
      <CategoryFilter tags={[]} selected={[]} onChange={vi.fn()} />,
    );
    expect(container.textContent).toBe("");
  });

  it("toggles a tag on and clears the selection", () => {
    const onChange = vi.fn();
    const { rerender } = render(
      <CategoryFilter tags={["api", "cms"]} selected={[]} onChange={onChange} />,
    );
    fireEvent.click(screen.getByText("api"));
    expect(onChange).toHaveBeenCalledWith(["api"]);

    // With a selection active, Clear resets it.
    rerender(<CategoryFilter tags={["api", "cms"]} selected={["api"]} onChange={onChange} />);
    fireEvent.click(screen.getByText("Clear"));
    expect(onChange).toHaveBeenCalledWith([]);
  });
});
