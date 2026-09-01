import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));

import { apiClient } from "../../../apiClient";
import { filesUpload } from "./filesApi";

const mocked = apiClient as unknown as { post: ReturnType<typeof vi.fn> };

// GH #1410: File Manager uploads must opt OUT of the client's default request
// timeout — a large file / slow uplink legitimately runs past it. Progress
// bounds the upload, not a fixed timeout.
describe("File Manager upload timeout (GH #1410)", () => {
  beforeEach(() => mocked.post.mockReset().mockResolvedValue({ data: {} }));

  it("single upload sends timeout: 0", async () => {
    const f = new File(["x"], "a.txt");
    await filesUpload("/home/u", f);
    const opts = mocked.post.mock.calls[0]?.[2] as { timeout?: number };
    expect(opts?.timeout).toBe(0);
  });
});
