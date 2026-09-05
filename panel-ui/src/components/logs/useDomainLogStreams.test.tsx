// useDomainLogStreams.test.tsx — neutral Module tests for the stream lifecycle
// (JAB-296): request shaping on open, and the "delete the key exactly once,
// tolerate an already-expired stream" close (AC3).
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";

vi.mock("../../apiClient", () => ({
  apiClient: { post: vi.fn(), delete: vi.fn() },
}));
vi.mock("../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn() } },
}));

import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback";
import { useDomainLogStreams } from "./useDomainLogStreams";

const mocked = apiClient as unknown as {
  post: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};
const toast = feedback as unknown as { message: { error: ReturnType<typeof vi.fn> } };

const OPENED = {
  data: { stream_key: "sk1", websocket_url: "wss://host/api/v1/logs/stream/sk1" },
};

beforeEach(() => {
  vi.clearAllMocks();
  mocked.post.mockResolvedValue(OPENED);
  mocked.delete.mockResolvedValue({});
});

describe("openStream", () => {
  it("posts a per-domain request and opens the modal (AC1)", async () => {
    const { result } = renderHook(() => useDomainLogStreams());
    await act(async () => {
      await result.current.openStream("access", "d1");
    });
    expect(mocked.post).toHaveBeenCalledWith("/logs/access", {
      log_type: "access",
      domain_id: "d1",
    });
    expect(result.current.modalProps.visible).toBe(true);
    expect(result.current.modalProps.streamUrl).toBe(OPENED.data.websocket_url);
    expect(result.current.modalProps.title).toBe("Access Log Stream");
    expect(result.current.modalProps.logType).toBe("access");
  });

  it("posts an aggregate request (no domain_id) when called with no domainId (AC1)", async () => {
    const { result } = renderHook(() => useDomainLogStreams());
    await act(async () => {
      await result.current.openStream("error");
    });
    expect(mocked.post).toHaveBeenCalledWith("/logs/access", { log_type: "error" });
    expect(result.current.modalProps.title).toBe("Error Log Stream");
  });

  it("surfaces the server error and stays closed on failure", async () => {
    mocked.post.mockRejectedValueOnce({ response: { data: { error: "no such domain" } } });
    const { result } = renderHook(() => useDomainLogStreams());
    await act(async () => {
      await result.current.openStream("access", "d1");
    });
    expect(toast.message.error).toHaveBeenCalledWith("no such domain");
    expect(result.current.modalProps.visible).toBe(false);
  });
});

describe("closeStream (AC3)", () => {
  it("deletes the stream key once and closes the modal", async () => {
    const { result } = renderHook(() => useDomainLogStreams());
    await act(async () => {
      await result.current.openStream("access", "d1");
    });
    await act(async () => {
      await result.current.modalProps.onClose();
    });
    expect(mocked.delete).toHaveBeenCalledTimes(1);
    expect(mocked.delete).toHaveBeenCalledWith("/logs/access/sk1");
    expect(result.current.modalProps.visible).toBe(false);
  });

  it("deletes exactly once even when closed twice in rapid succession", async () => {
    const { result } = renderHook(() => useDomainLogStreams());
    await act(async () => {
      await result.current.openStream("access", "d1");
    });
    const onClose = result.current.modalProps.onClose;
    await act(async () => {
      await Promise.all([onClose(), onClose()]);
    });
    expect(mocked.delete).toHaveBeenCalledTimes(1);
  });

  it("tolerates an already-expired stream (DELETE rejects)", async () => {
    mocked.delete.mockRejectedValueOnce(new Error("410 gone"));
    const { result } = renderHook(() => useDomainLogStreams());
    await act(async () => {
      await result.current.openStream("access", "d1");
    });
    await act(async () => {
      await expect(result.current.modalProps.onClose()).resolves.toBeUndefined();
    });
    expect(result.current.modalProps.visible).toBe(false);
  });

  it("issues no DELETE when nothing was opened", async () => {
    const { result } = renderHook(() => useDomainLogStreams());
    await act(async () => {
      await result.current.modalProps.onClose();
    });
    expect(mocked.delete).not.toHaveBeenCalled();
  });
});
