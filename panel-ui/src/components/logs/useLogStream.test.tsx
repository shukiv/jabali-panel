// useLogStream.test.tsx — the JAB-296 neutral Module tests for the stream
// lifecycle (AC5): connect/close transitions, newline-stripped append,
// pause/resume buffering (which the old .tsx got wrong), the error path, and
// the GoAccess polling cadence. A fake WebSocket stands in for the browser
// socket; fake timers drive the GoAccess poll.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act } from "@testing-library/react";

vi.mock("../../lib/feedback", () => ({
  feedback: {
    message: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
  },
}));

import { feedback } from "../../lib/feedback";
import { useLogStream } from "./useLogStream";

const toast = feedback as unknown as {
  message: {
    success: ReturnType<typeof vi.fn>;
    info: ReturnType<typeof vi.fn>;
    error: ReturnType<typeof vi.fn>;
  };
};

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  url: string;
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onclose: ((e: { code: number; reason?: string }) => void) | null = null;
  onerror: ((e: unknown) => void) | null = null;
  close = vi.fn();
  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }
}

const lastSocket = () => FakeWebSocket.instances[FakeWebSocket.instances.length - 1];

beforeEach(() => {
  vi.clearAllMocks();
  FakeWebSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("connect + message (AC5)", () => {
  it("opens a socket to the stream URL and reports connecting until onopen", () => {
    const { result } = renderHook(() =>
      useLogStream({ streamUrl: "wss://host/s/1", logType: "access", active: true }),
    );
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(lastSocket().url).toBe("wss://host/s/1");
    expect(result.current.connecting).toBe(true);
    expect(result.current.connected).toBe(false);

    act(() => lastSocket().onopen?.());
    expect(result.current.connected).toBe(true);
    expect(result.current.connecting).toBe(false);
    expect(toast.message.success).toHaveBeenCalledWith("Connected to log stream");
  });

  it("appends incoming frames with a single trailing newline stripped", () => {
    const { result } = renderHook(() =>
      useLogStream({ streamUrl: "wss://host/s/1", logType: "access", active: true }),
    );
    act(() => lastSocket().onopen?.());
    act(() => lastSocket().onmessage?.({ data: "GET / 200\n" }));
    act(() => lastSocket().onmessage?.({ data: "GET /x 404\r\n" }));
    expect(result.current.logs).toEqual(["GET / 200", "GET /x 404"]);
  });

  it("does not open a socket for goaccess (iframe-rendered)", () => {
    renderHook(() =>
      useLogStream({ streamUrl: "wss://host/s/1", logType: "goaccess", active: true }),
    );
    expect(FakeWebSocket.instances).toHaveLength(0);
  });
});

describe("pause / resume buffering (AC5 — the fixed bug)", () => {
  it("holds frames while paused and flushes them on resume", () => {
    const { result } = renderHook(() =>
      useLogStream({ streamUrl: "wss://host/s/1", logType: "access", active: true }),
    );
    act(() => lastSocket().onopen?.());
    act(() => lastSocket().onmessage?.({ data: "live-1" }));
    expect(result.current.logs).toEqual(["live-1"]);

    act(() => result.current.togglePause());
    expect(result.current.paused).toBe(true);

    // Frames arriving while paused must NOT reach the view. Under the original
    // stale-closure bug they appended anyway; this is the assertion that fails
    // if the onmessage handler reads paused state instead of the ref.
    act(() => lastSocket().onmessage?.({ data: "paused-1" }));
    act(() => lastSocket().onmessage?.({ data: "paused-2" }));
    // Frames arriving while paused never reach the view, but the live buffered
    // count reflects them (drives the "+N paused" indicator).
    expect(result.current.logs).toEqual(["live-1"]);
    expect(result.current.bufferedCount).toBe(2);

    // Resume flushes exactly the two buffered frames and clears the count.
    act(() => result.current.togglePause());
    expect(result.current.paused).toBe(false);
    expect(result.current.logs).toEqual(["live-1", "paused-1", "paused-2"]);
    expect(result.current.bufferedCount).toBe(0);
  });

  it("clear empties the view and the buffer", () => {
    const { result } = renderHook(() =>
      useLogStream({ streamUrl: "wss://host/s/1", logType: "access", active: true }),
    );
    act(() => lastSocket().onopen?.());
    act(() => lastSocket().onmessage?.({ data: "a" }));
    act(() => result.current.clear());
    expect(result.current.logs).toEqual([]);
  });
});

describe("close / error transitions (AC5)", () => {
  it("treats a clean 1000 close as 'stream ended'", () => {
    const { result } = renderHook(() =>
      useLogStream({ streamUrl: "wss://host/s/1", logType: "access", active: true }),
    );
    act(() => lastSocket().onopen?.());
    act(() => lastSocket().onclose?.({ code: 1000 }));
    expect(result.current.connected).toBe(false);
    expect(toast.message.info).toHaveBeenCalledWith("Log stream ended");
    expect(toast.message.error).not.toHaveBeenCalled();
  });

  it("treats an abnormal 1006 close as a lost connection", () => {
    renderHook(() =>
      useLogStream({ streamUrl: "wss://host/s/1", logType: "access", active: true }),
    );
    act(() => lastSocket().onopen?.());
    act(() => lastSocket().onclose?.({ code: 1006 }));
    expect(toast.message.error).toHaveBeenCalledWith("Connection lost");
    expect(toast.message.info).not.toHaveBeenCalled();
  });

  it("surfaces a socket error", () => {
    const { result } = renderHook(() =>
      useLogStream({ streamUrl: "wss://host/s/1", logType: "access", active: true }),
    );
    act(() => lastSocket().onerror?.(new Event("error")));
    expect(toast.message.error).toHaveBeenCalledWith("WebSocket connection error");
    expect(result.current.connecting).toBe(false);
  });
});

describe("GoAccess polling cadence (AC5)", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("bumps the tick and snapshots scroll every 10s, not before", () => {
    const onBeforeGoAccessRefresh = vi.fn();
    const { result } = renderHook(() =>
      useLogStream({
        streamUrl: "wss://host/s/1",
        logType: "goaccess",
        active: true,
        onBeforeGoAccessRefresh,
      }),
    );
    const first = result.current.goAccessTick;

    act(() => vi.advanceTimersByTime(9999));
    expect(onBeforeGoAccessRefresh).not.toHaveBeenCalled();
    expect(result.current.goAccessTick).toBe(first);

    act(() => vi.advanceTimersByTime(1));
    expect(onBeforeGoAccessRefresh).toHaveBeenCalledTimes(1);
    expect(result.current.goAccessTick).not.toBe(first);
  });

  it("does not poll when inactive", () => {
    const onBeforeGoAccessRefresh = vi.fn();
    renderHook(() =>
      useLogStream({
        streamUrl: "wss://host/s/1",
        logType: "goaccess",
        active: false,
        onBeforeGoAccessRefresh,
      }),
    );
    act(() => vi.advanceTimersByTime(30000));
    expect(onBeforeGoAccessRefresh).not.toHaveBeenCalled();
  });
});
