// GH #1410: the File Manager upload uses the same 80 MB chunk size as the DB
// restore, and resumes by byte offset so a chunk-size change can't 409-loop a
// partial upload. These tests pin both so the size can't silently drift back
// (the earlier fix changed only filesApi's default and the call site kept 10 MB).
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { apiClient, UPLOAD_CHUNK_BYTES, UPLOAD_SINGLE_SHOT_MAX } from "../../../apiClient";
import { filesUploadChunked } from "./filesApi";

type Sent = { offset: number; final: boolean; len: number };

function mkFile(bytes: number): File {
  return new File([new Uint8Array(bytes)], "f.bin", { lastModified: 1 });
}

function recordPost(sent: Sent[]) {
  return vi.spyOn(apiClient, "post").mockImplementation((async (url: string, body: unknown) => {
    const q = new URLSearchParams(url.slice(url.indexOf("?") + 1));
    sent.push({
      offset: Number(q.get("offset")),
      final: q.get("final") === "1",
      len: (body as Blob).size,
    });
    return { data: {} };
  }) as never);
}

beforeEach(() => {
  try {
    localStorage.clear();
  } catch {
    /* ignore */
  }
});
afterEach(() => vi.restoreAllMocks());

describe("GH #1410 — File Manager upload chunking", () => {
  it("uses the same 80 MB chunk size as the DB restore", () => {
    expect(UPLOAD_CHUNK_BYTES).toBe(80 * 1024 * 1024);
    expect(UPLOAD_SINGLE_SHOT_MAX).toBe(90 * 1024 * 1024);
  });

  it("splits a file into contiguous chunks, flagging only the last final", async () => {
    const sent: Sent[] = [];
    recordPost(sent);
    await filesUploadChunked("/home/a", mkFile(10), 4); // 10 bytes @ 4-byte chunks
    expect(sent.map((s) => s.offset)).toEqual([0, 4, 8]);
    expect(sent.map((s) => s.len)).toEqual([4, 4, 2]);
    expect(sent.map((s) => s.final)).toEqual([false, false, true]);
  });

  it("resumes at the server's exact byte offset even if it isn't chunk-aligned", async () => {
    const sent: Sent[] = [];
    recordPost(sent);
    // A prior (differently-chunked) session left 5 bytes staged.
    vi.spyOn(apiClient, "get").mockResolvedValue({ data: { written: 5 } } as never);
    localStorage.setItem("jabali:upload:/home/a|f.bin|10|1", "resume-uuid");

    await filesUploadChunked("/home/a", mkFile(10), 4);
    // Resume from 5 (not floored to 4) → no 409 bad_offset loop.
    expect(sent[0].offset).toBe(5);
    expect(sent.map((s) => s.offset)).toEqual([5, 9]);
    expect(sent[sent.length - 1].final).toBe(true);
  });

  it("restarts under a fresh id when the staged size is past the file", async () => {
    const sent: Sent[] = [];
    recordPost(sent);
    // Corrupt/foreign status: server reports more bytes than the file has.
    vi.spyOn(apiClient, "get").mockResolvedValue({ data: { written: 999 } } as never);
    localStorage.setItem("jabali:upload:/home/a|f.bin|10|1", "stale-uuid");

    await filesUploadChunked("/home/a", mkFile(10), 4);
    // Starts clean from 0, does not send a bad offset.
    expect(sent.map((s) => s.offset)).toEqual([0, 4, 8]);
    expect(localStorage.getItem("jabali:upload:/home/a|f.bin|10|1")).not.toBe("stale-uuid");
  });

  it("finalises a fully-staged resume by sending the final marker", async () => {
    const sent: Sent[] = [];
    recordPost(sent);
    vi.spyOn(apiClient, "get").mockResolvedValue({ data: { written: 10 } } as never);
    localStorage.setItem("jabali:upload:/home/a|f.bin|10|1", "resume-uuid");

    await filesUploadChunked("/home/a", mkFile(10), 4);
    // Every byte already landed, but the final marker hadn't — one empty final.
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({ offset: 10, final: true, len: 0 });
  });
});
