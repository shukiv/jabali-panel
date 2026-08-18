import { describe, it, expect, vi, afterEach } from "vitest";
import { kratosClient, redeemRecoveryCode } from "./kratos";

// JAB-274: redeemRecoveryCode must trust the SESSION, not the redemption POST's
// HTTP status — a duplicate submit (StrictMode / prefetch / preview bot) of a
// link whose first submit already signed the user in comes back as a followed
// 303 → 2xx, which must NOT be reported as "invalid or already used".

const blcrError = {
  response: {
    status: 422,
    data: { error: { id: "browser_location_change_required" } },
  },
};

function mockWhoami(signedIn: boolean) {
  vi.spyOn(kratosClient, "get").mockImplementation((url: string) => {
    if (url === "/sessions/whoami") {
      return signedIn
        ? Promise.resolve({ data: { id: "sess", active: true } })
        : Promise.reject({ response: { status: 401 } });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

afterEach(() => vi.restoreAllMocks());

describe("redeemRecoveryCode", () => {
  it("missing flow/code → error, without touching Kratos", async () => {
    const post = vi.spyOn(kratosClient, "post");
    const res = await redeemRecoveryCode("", "123");
    expect(res.kind).toBe("error");
    expect(post).not.toHaveBeenCalled();
  });

  it("fresh success (422 browser_location_change_required) → ok", async () => {
    vi.spyOn(kratosClient, "post").mockRejectedValue(blcrError);
    const whoami = vi.spyOn(kratosClient, "get");
    const res = await redeemRecoveryCode("flow", "code");
    expect(res.kind).toBe("ok");
    // A clean blcr success needs no session probe.
    expect(whoami).not.toHaveBeenCalled();
  });

  it("duplicate submit → followed 303 → 2xx, session exists → ok (the JAB-274 bug)", async () => {
    vi.spyOn(kratosClient, "post").mockResolvedValue({ status: 200, data: {} });
    mockWhoami(true);
    const res = await redeemRecoveryCode("flow", "code");
    expect(res.kind).toBe("ok");
  });

  it("2xx re-render but no session (genuinely rejected code) → error", async () => {
    vi.spyOn(kratosClient, "post").mockResolvedValue({ status: 200, data: {} });
    mockWhoami(false);
    const res = await redeemRecoveryCode("flow", "code");
    expect(res.kind).toBe("error");
    if (res.kind === "error") expect(res.message).toMatch(/invalid or has already been used/i);
  });

  it("410 → expired error (no session rescue)", async () => {
    vi.spyOn(kratosClient, "post").mockRejectedValue({ response: { status: 410 } });
    const res = await redeemRecoveryCode("flow", "code");
    expect(res.kind).toBe("error");
    if (res.kind === "error") expect(res.message).toMatch(/expired/i);
  });

  it("400 non-blcr but session already established → ok", async () => {
    vi.spyOn(kratosClient, "post").mockRejectedValue({ response: { status: 400, data: {} } });
    mockWhoami(true);
    const res = await redeemRecoveryCode("flow", "code");
    expect(res.kind).toBe("ok");
  });

  it("400 non-blcr and no session → invalid error", async () => {
    vi.spyOn(kratosClient, "post").mockRejectedValue({ response: { status: 400, data: {} } });
    mockWhoami(false);
    const res = await redeemRecoveryCode("flow", "code");
    expect(res.kind).toBe("error");
    if (res.kind === "error") expect(res.message).toMatch(/invalid or has already been used/i);
  });

  it("whoami itself blips (5xx) → surface the error, don't mask as success", async () => {
    vi.spyOn(kratosClient, "post").mockResolvedValue({ status: 200, data: {} });
    vi.spyOn(kratosClient, "get").mockRejectedValue({ response: { status: 503 } });
    const res = await redeemRecoveryCode("flow", "code");
    expect(res.kind).toBe("error");
  });
});
