// DomainSettingsButton.addruleux.test.tsx — GH #1624 Add Rule UX (lxsdevcode
// follow-up on #1717). After picking a rule type, the new rule must be obvious
// without hunting the bottom of a long list: it auto-expands + scrolls into view
// (already shipped), and now the first field is focused and the row is briefly
// highlighted. Also, the rule-type label must never break mid-word ("Rew\nrit\ne")
// when a long value squeezes the row, so it is nowrap + non-shrinking.
//
// Only apiClient is mocked; the real AntD widgets + in-file RuleBuilder render.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  TenantNginxRulesPanel,
  type DomainSettingsTarget,
} from "./DomainSettingsButton";

vi.mock("../apiClient", () => ({ apiClient: { patch: vi.fn() } }));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as { patch: ReturnType<typeof vi.fn> };

function renderTenant(domain: DomainSettingsTarget) {
  const qc = new QueryClient();
  render(
    <QueryClientProvider client={qc}>
      <App>
        <TenantNginxRulesPanel domain={domain} />
      </App>
    </QueryClientProvider>,
  );
}

const domain: DomainSettingsTarget = { id: "d1", name: "example.com", nginx_rules: [] };

async function addRewriteRule() {
  fireEvent.click(screen.getByRole("button", { name: /Add Rule/i }));
  const rewriteOption = await screen.findByText("Rewrite");
  fireEvent.click(rewriteOption);
}

describe("GH #1624 — Add Rule UX", () => {
  beforeEach(() => {
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
  });

  it("focuses the first field of the freshly added rule", async () => {
    renderTenant(domain);
    await addRewriteRule();

    // The Rewrite body's first field is the Pattern input (placeholder ^/old$).
    // Option B: after adding, focus lands there so the user can type immediately.
    await waitFor(() => {
      const active = document.activeElement as HTMLElement | null;
      expect(active?.tagName).toBe("INPUT");
      expect(active?.getAttribute("placeholder")).toBe("^/old$");
    });
  });

  it("flashes the freshly added rule with a highlight background", async () => {
    renderTenant(domain);
    await addRewriteRule();

    // Option B: the new row briefly gets a primary-tint background so the eye
    // lands on it (fades back after 1.5s — the fade itself is visual only).
    await waitFor(() => {
      const flashed = Array.from(
        document.querySelectorAll<HTMLElement>(".ant-card"),
      ).some((card) => card.style.background !== "");
      expect(flashed, "a rule card carries the highlight background").toBe(true);
    });
  });

  it("renders the rule-type label as non-breaking (nowrap, non-shrinking)", async () => {
    renderTenant(domain);
    await addRewriteRule();

    // Two "Rewrite" texts exist (the picker card + the row label). antd's
    // `strong` wraps the text in <strong> inside the styled <span.ant-typography>
    // flex item, so the no-break style lives on the parent span. The row label
    // is the one whose styled span carries it.
    await waitFor(() => {
      const styledSpan = screen
        .getAllByText("Rewrite")
        .map((el) => el.closest("span.ant-typography") as HTMLElement | null)
        .find((span) => span?.style.whiteSpace === "nowrap");
      expect(styledSpan, "row type label span with nowrap").toBeTruthy();
      expect(styledSpan!.style.flexShrink).toBe("0");
    });
  });
});
