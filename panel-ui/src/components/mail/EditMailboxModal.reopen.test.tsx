// EditMailboxModal.reopen.test.tsx — GH #1615.
//
// Guards that the Edit-mailbox drawer's Account form reflects the CURRENT
// mailbox every time it opens — the reported bug was a blank/stale form on a
// second open. The fix drives the prefill via the Form's initialValues +
// clearOnDestroy (re-read on each fresh mount) instead of a
// useEffect(setFieldsValue), which antd's FAQ warns against for a Form in a
// lazily-rendered overlay.
//
// Note: jsdom mounts overlay children synchronously, so these do NOT reproduce
// the original blank-on-reopen (that needs a real browser's lazy render). They
// pin the fixed behavior and catch a regression to the effect-based prefill.
//
// Only apiClient is mocked; the real AntD Drawer/Form/Input run. The default
// "Account" tab is under test; the other tabs are not force-rendered, so their
// data-fetching sections never mount here.
import { useState } from "react";
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { EditMailboxModal } from "./EditMailboxModal";
import type { Mailbox } from "../../hooks/useMailboxes";

vi.mock("../../apiClient", () => ({ apiClient: { patch: vi.fn(), get: vi.fn() } }));

function mailbox(overrides: Partial<Mailbox>): Mailbox {
  return {
    id: "mb1",
    domain_id: "d1",
    email: "alice@example.com",
    display_name: "Old Name",
    quota_bytes: 256 * 1024 * 1024,
    is_disabled: false,
    send_only: false,
    last_usage_bytes: 0,
    created_at: "",
    updated_at: "",
    ...overrides,
  };
}

function renderModal(props: { open: boolean; mailbox: Mailbox | null }) {
  const qc = new QueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <App>
        <EditMailboxModal open={props.open} mailbox={props.mailbox} onClose={() => {}} />
      </App>
    </QueryClientProvider>,
  );
}

// The display-name field is an Input; read its live value by role+name.
function displayNameInput(): HTMLInputElement {
  return screen.getByRole("textbox", { name: /display name/i }) as HTMLInputElement;
}

describe("EditMailboxModal reopen prefill (GH #1615)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("shows the current display name every time it reopens (not blank/stale)", async () => {
    const { rerender } = renderModal({ open: true, mailbox: mailbox({ display_name: "Old Name" }) });
    await waitFor(() => expect(displayNameInput().value).toBe("Old Name"));

    // Close the drawer (destroyOnHidden unmounts the form).
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <App>
          <EditMailboxModal open={false} mailbox={null} onClose={() => {}} />
        </App>
      </QueryClientProvider>,
    );

    // Reopen with the UPDATED mailbox — the field must reflect the new value.
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <App>
          <EditMailboxModal
            open
            mailbox={mailbox({ display_name: "New Name" })}
            onClose={() => {}}
          />
        </App>
      </QueryClientProvider>,
    );

    await waitFor(() => expect(displayNameInput().value).toBe("New Name"));
  });

  // Faithful reproduction of the real MailboxesTab flow with a SINGLE persistent
  // EditMailboxModal instance (so the useForm() store survives across opens):
  // open → change name → Save → close → reopen shows the updated value.
  it("reflects the updated name on reopen after an in-place save", async () => {
    const mocked = (await import("../../apiClient")).apiClient as unknown as {
      patch: ReturnType<typeof vi.fn>;
    };
    mocked.patch.mockResolvedValue({ data: mailbox({ display_name: "New Name" }) });

    function Harness() {
      // Mirrors MailboxesTab: editTarget state, opened via a button, and the
      // "row" reflects the latest saved value (as a list refetch would).
      const [current, setCurrent] = useState(mailbox({ display_name: "Old Name" }));
      const [target, setTarget] = useState<Mailbox | null>(null);
      return (
        <>
          <button onClick={() => setTarget(current)}>open-edit</button>
          <EditMailboxModal
            open={target !== null}
            mailbox={target}
            onClose={() => {
              // Simulate the list refetch surfacing the saved value.
              setCurrent(mailbox({ display_name: "New Name" }));
              setTarget(null);
            }}
          />
        </>
      );
    }

    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <App>
          <Harness />
        </App>
      </QueryClientProvider>,
    );

    // Open, confirm the old value, change it, Save.
    fireEvent.click(screen.getByText("open-edit"));
    await waitFor(() => expect(displayNameInput().value).toBe("Old Name"));
    fireEvent.change(displayNameInput(), { target: { value: "New Name" } });
    fireEvent.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());

    // Reopen — the field must show the saved value, not blank or the old one.
    await waitFor(() => expect(screen.queryByRole("textbox", { name: /display name/i })).toBeNull());
    fireEvent.click(screen.getByText("open-edit"));
    await waitFor(() => expect(displayNameInput().value).toBe("New Name"));
  });
});
