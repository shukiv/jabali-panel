// The footer invites people to translate Jabali on Weblate, in both shells.
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { JabaliFooter } from "./JabaliFooter";
import { ThemeModeProvider } from "../theme/ThemeModeContext";

describe("JabaliFooter", () => {
  it("links to the Weblate project in a new tab", () => {
    render(
      <ThemeModeProvider>
        <JabaliFooter />
      </ThemeModeProvider>,
    );
    const link = screen.getByRole("link", { name: "Translate Jabali into your language" });
    expect(link.getAttribute("href")).toBe("https://translate.jabali-panel.com/engage/jabali-panel/");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toContain("noreferrer");
  });
});
