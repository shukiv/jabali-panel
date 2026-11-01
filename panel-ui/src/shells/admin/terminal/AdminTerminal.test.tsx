// AdminTerminal: a connection that fails has to stay visible. A failed
// WebSocket fires error and then close, and the close must not hide the error.
import { act, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    loadAddon() {}
    open() {}
    focus() {}
    write() {}
    onData() {}
    onResize() {}
    dispose() {}
  },
}));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));
vi.mock("@xterm/xterm/css/xterm.css", () => ({}));
vi.mock("../../../apiClient", () => ({
  apiClient: { post: vi.fn(async () => ({ data: { websocket_url: "wss://panel.test/ws" } })) },
}));
vi.mock("../../../hooks/useRootTerminalEnabled", () => ({
  useRootTerminalEnabled: () => ({ enabled: true, isLoading: false }),
}));

import { AdminTerminal } from "./AdminTerminal";

class FakeSocket {
  static OPEN = 1;
  static last: FakeSocket | null = null;
  readyState = 0;
  binaryType = "";
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: ArrayBuffer }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  constructor(public url: string) {
    FakeSocket.last = this;
  }
  send() {}
  close() {}
}

describe("AdminTerminal", () => {
  beforeEach(() => {
    FakeSocket.last = null;
    vi.stubGlobal("WebSocket", FakeSocket);
  });

  it("keeps the error on screen after the failed socket closes", async () => {
    render(<AdminTerminal />);
    await waitFor(() => expect(FakeSocket.last?.onclose).toBeTruthy());

    act(() => {
      FakeSocket.last?.onerror?.();
      FakeSocket.last?.onclose?.();
    });

    expect(await screen.findByText(/WebSocket error/)).toBeTruthy();
  });

  it("shows no error when an open session closes", async () => {
    render(<AdminTerminal />);
    await waitFor(() => expect(FakeSocket.last?.onclose).toBeTruthy());

    act(() => {
      FakeSocket.last?.onopen?.();
      FakeSocket.last?.onclose?.();
    });

    expect(screen.queryByText(/WebSocket error/)).toBeNull();
  });
});
