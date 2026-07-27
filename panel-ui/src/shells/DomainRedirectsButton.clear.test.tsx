// DomainRedirectsButton.clear.test.tsx — GH #717.
//
// Turning the whole-domain redirect OFF ("back to normal") must actually clear
// it. The bug: the modal sent `redirect_all_to: null`, but the API field is a
// Go `*string` that can't tell a JSON null apart from an omitted field — both
// unmarshal to nil, which the handler treats as "no change", so the redirect
// was never removed. The fix sends an empty string, which the handler clears on.
//
// Only apiClient is mocked; the real AntD Modal/Switch run.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DomainRedirectsButton } from "./DomainRedirectsButton";

vi.mock("../apiClient", () => ({ apiClient: { patch: vi.fn() } }));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as { patch: ReturnType<typeof vi.fn> };

function renderBtn() {
  const qc = new QueryClient();
  render(
    <QueryClientProvider client={qc}>
      <App>
        <DomainRedirectsButton
          domain={{
            id: "d1",
            name: "old.example.com",
            redirect_all_to: "https://old.example.com",
            redirect_all_type: "301",
            page_redirects: [],
          }}
          open
          onClose={() => {}}
        />
      </App>
    </QueryClientProvider>,
  );
}

describe("DomainRedirectsButton clear (GH #717)", () => {
  beforeEach(() => {
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
  });

  it("sends empty strings (NOT null) to clear the whole-domain redirect", async () => {
    renderBtn();
    // The whole-domain toggle starts ON (redirect_all_to is set). Turn it OFF.
    fireEvent.click(screen.getByRole("switch"));
    fireEvent.click(screen.getByText("Save Redirects"));

    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    const [url, body] = mocked.patch.mock.calls[0];
    expect(url).toBe("/domains/d1");
    // The exact regression: must be "" so the API clears it — null would be
    // silently ignored as "no change" and the redirect would persist.
    expect(body.redirect_all_to).toBe("");
    expect(body.redirect_all_type).toBe("");
  });
});
