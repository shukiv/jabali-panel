// JAB-137: the Updates warning card keeps the core cautions always visible but
// collapses the detailed SSH recovery commands behind a toggle (collapsed by
// default) so the update controls aren't pushed below the fold. No safety
// content is removed — only progressively disclosed.
import { render, screen, fireEvent } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { UpdateWarningAlert } from "./SystemUpdatesPage";

describe("UpdateWarningAlert (JAB-137)", () => {
  it("shows the core cautions but hides recovery commands until toggled", () => {
    render(<UpdateWarningAlert />);

    // Core caution is always visible.
    expect(screen.getByText(/SSH access as root/i)).toBeInTheDocument();

    // Recovery commands are collapsed by default.
    expect(screen.queryByText(/jabali repair --diagnose/)).not.toBeInTheDocument();

    // One click reveals them...
    fireEvent.click(screen.getByText("Show recovery steps"));
    expect(screen.getByText(/jabali repair --diagnose/)).toBeInTheDocument();

    // ...and the toggle collapses them again.
    fireEvent.click(screen.getByText("Hide recovery steps"));
    expect(screen.queryByText(/jabali repair --diagnose/)).not.toBeInTheDocument();
  });
});
