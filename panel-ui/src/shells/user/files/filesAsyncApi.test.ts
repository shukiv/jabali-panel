import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));

import { apiClient } from "../../../apiClient";
import { filesExtractStart, filesJobStatus } from "./filesApi";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

// GH #1392 — the async-extract client helpers.
describe("filesExtractStart", () => {
  beforeEach(() => {
    mocked.get.mockReset();
    mocked.post.mockReset();
  });

  it("posts the archive with async=1 and returns the job id", async () => {
    mocked.post.mockResolvedValue({ data: { job_id: "abc123" } });
    const out = await filesExtractStart("/home/u/big.zip");
    expect(out).toEqual({ job_id: "abc123" });
    expect(mocked.post).toHaveBeenCalledWith(
      "/files/extract",
      { path: "/home/u/big.zip", dest: undefined },
      { params: { async: 1 } },
    );
  });
});

describe("filesJobStatus", () => {
  beforeEach(() => {
    mocked.get.mockReset();
    mocked.post.mockReset();
  });

  it("GETs the job by url-encoded id and returns its status", async () => {
    mocked.get.mockResolvedValue({
      data: {
        job_id: "j1",
        status: "running",
        done: 2,
        total: 10,
        result: { dest: "", extracted: 0, skipped: 0 },
        started_at: "2026-08-31T00:00:00Z",
      },
    });
    const s = await filesJobStatus("j 1/x");
    expect(mocked.get).toHaveBeenCalledWith("/files/jobs/j%201%2Fx");
    expect(s.status).toBe("running");
    expect(s.total).toBe(10);
  });
});
