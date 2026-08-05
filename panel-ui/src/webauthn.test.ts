// webauthn.test.ts — GH #917. The encode/decode + result JSON shape MUST match
// Kratos's own webauthn.js exactly, or authentication silently breaks. These
// tests pin that contract (verified against the deployed v26.2.0 script).
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  bufferDecode,
  bufferEncode,
  flowHasPasskeyEnrol,
  flowHasPasskeyLogin,
  flowHasWebauthn2faLogin,
  flowHasWebauthnEnrol,
  passkeyEnrolFields,
  passkeyLoginFields,
  webauthn2faLoginFields,
  webauthnEnrolFields,
} from "./webauthn";
import type { KratosFlow } from "./kratos";

// A flow with a single input node (name/value) in the given group. Used to build
// the webauthn 2FA trigger-node flows (options live in the *_trigger value).
function nodeFlow(group: string, name: string, value: string, type = "hidden"): KratosFlow {
  return {
    id: "n1",
    type: "browser",
    expires_at: "",
    issued_at: "",
    request_url: "",
    ui: {
      action: "/self-service/x?flow=n1",
      method: "POST",
      nodes: [
        {
          type: "input",
          group,
          attributes: { name, type: type as never, value },
        },
      ],
    },
  };
}

function u8(...bytes: number[]): ArrayBuffer {
  return new Uint8Array(bytes).buffer;
}

// Minimal login flow carrying a passkey discoverable-login challenge, exactly
// as Kratos serialises it: { publicKey: { challenge: <b64url>, ... } }.
function loginFlow(challengeB64url: string): KratosFlow {
  return {
    id: "f1",
    type: "browser",
    expires_at: "",
    issued_at: "",
    request_url: "",
    ui: {
      action: "/self-service/login?flow=f1",
      method: "POST",
      nodes: [
        {
          type: "input",
          group: "passkey",
          attributes: {
            name: "passkey_challenge",
            type: "hidden",
            value: JSON.stringify({
              publicKey: { challenge: challengeB64url, allowCredentials: [], timeout: 60000 },
            }),
          },
        },
      ],
    },
  };
}

function enrolFlow(createData: unknown): KratosFlow {
  return {
    id: "s1",
    type: "browser",
    expires_at: "",
    issued_at: "",
    request_url: "",
    ui: {
      action: "/self-service/settings?flow=s1",
      method: "POST",
      nodes: [
        {
          type: "input",
          group: "passkey",
          attributes: {
            name: "passkey_create_data",
            type: "hidden",
            value: JSON.stringify(createData),
          },
        },
      ],
    },
  };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("base64url codec (mirrors Kratos webauthn.js)", () => {
  it("encodes to URL-safe base64 without padding", () => {
    // bytes 0xfb 0xff → std base64 "+/8=" → url "-_8" (no padding)
    expect(bufferEncode(u8(0xfb, 0xff))).toBe("-_8");
    // bytes 1,2,3 → "AQID" (no + / or padding involved)
    expect(bufferEncode(u8(1, 2, 3))).toBe("AQID");
  });

  it("returns empty string for null/undefined (userHandle can be null)", () => {
    expect(bufferEncode(null)).toBe("");
    expect(bufferEncode(undefined)).toBe("");
  });

  it("decodes URL-safe base64 (padded or not) back to bytes", () => {
    expect(Array.from(bufferDecode("-_8"))).toEqual([0xfb, 0xff]);
    expect(Array.from(bufferDecode("AQID"))).toEqual([1, 2, 3]);
  });

  it("round-trips arbitrary bytes", () => {
    const bytes = [0, 1, 62, 63, 127, 128, 200, 254, 255];
    expect(Array.from(bufferDecode(bufferEncode(new Uint8Array(bytes).buffer)))).toEqual(bytes);
  });
});

describe("flow detection", () => {
  it("detects a passkey login challenge", () => {
    expect(flowHasPasskeyLogin(loginFlow("AQID"))).toBe(true);
    expect(flowHasPasskeyEnrol(loginFlow("AQID"))).toBe(false);
  });
  it("detects a passkey enrolment create-data node", () => {
    expect(flowHasPasskeyEnrol(enrolFlow({ publicKey: {} }))).toBe(true);
    expect(flowHasPasskeyLogin(enrolFlow({ publicKey: {} }))).toBe(false);
  });
});

describe("passkeyLoginFields", () => {
  it("returns null when the flow has no passkey challenge", async () => {
    const flow = loginFlow("AQID");
    flow.ui.nodes = [];
    expect(await passkeyLoginFields(flow)).toBeNull();
  });

  it("runs credentials.get and encodes the assertion exactly like Kratos", async () => {
    const get = vi.fn().mockResolvedValue({
      id: "cred-id",
      type: "public-key",
      rawId: u8(1, 2, 3),
      response: {
        authenticatorData: u8(4, 5, 6),
        clientDataJSON: u8(7, 8, 9),
        signature: u8(10, 11, 12),
        userHandle: u8(13, 14, 15),
      },
    });
    vi.stubGlobal("navigator", { credentials: { get, create: vi.fn() } });
    vi.stubGlobal("PublicKeyCredential", function () {});

    const out = await passkeyLoginFields(loginFlow("AQID"));
    expect(out).not.toBeNull();
    expect(out!.identifier).toBe(""); // discoverable login: no username typed

    // The challenge must reach the authenticator as decoded bytes.
    const passed = get.mock.calls[0][0].publicKey;
    expect(Array.from(new Uint8Array(passed.challenge))).toEqual([1, 2, 3]); // "AQID"

    const body = JSON.parse(out!.passkey_login);
    expect(body).toEqual({
      id: "cred-id",
      rawId: bufferEncode(u8(1, 2, 3)),
      type: "public-key",
      response: {
        authenticatorData: bufferEncode(u8(4, 5, 6)),
        clientDataJSON: bufferEncode(u8(7, 8, 9)),
        signature: bufferEncode(u8(10, 11, 12)),
        userHandle: bufferEncode(u8(13, 14, 15)),
      },
    });
  });

  it("encodes a null userHandle as an empty string", async () => {
    const get = vi.fn().mockResolvedValue({
      id: "c",
      type: "public-key",
      rawId: u8(1),
      response: {
        authenticatorData: u8(2),
        clientDataJSON: u8(3),
        signature: u8(4),
        userHandle: null,
      },
    });
    vi.stubGlobal("navigator", { credentials: { get, create: vi.fn() } });
    const out = await passkeyLoginFields(loginFlow("AQID"));
    expect(JSON.parse(out!.passkey_login).response.userHandle).toBe("");
  });
});

describe("passkeyEnrolFields", () => {
  it("runs credentials.create and encodes the attestation like Kratos", async () => {
    const create = vi.fn().mockResolvedValue({
      id: "new-cred",
      type: "public-key",
      rawId: u8(9, 9, 9),
      response: {
        attestationObject: u8(1, 1, 1),
        clientDataJSON: u8(2, 2, 2),
      },
    });
    vi.stubGlobal("navigator", { credentials: { get: vi.fn(), create } });

    const flow = enrolFlow({
      publicKey: {
        user: { id: "AQID", name: "alice", displayName: "alice" },
        challenge: "BAUG", // bytes 4,5,6
        excludeCredentials: [],
        pubKeyCredParams: [{ type: "public-key", alg: -7 }],
      },
    });
    const out = await passkeyEnrolFields(flow);
    expect(out).not.toBeNull();

    // user.id + challenge must be decoded to bytes before create.
    const passed = create.mock.calls[0][0].publicKey;
    expect(Array.from(new Uint8Array(passed.user.id))).toEqual([1, 2, 3]);
    expect(Array.from(new Uint8Array(passed.challenge))).toEqual([4, 5, 6]);

    const body = JSON.parse(out!.passkey_settings_register);
    expect(body).toEqual({
      id: "new-cred",
      rawId: bufferEncode(u8(9, 9, 9)),
      type: "public-key",
      response: {
        attestationObject: bufferEncode(u8(1, 1, 1)),
        clientDataJSON: bufferEncode(u8(2, 2, 2)),
      },
    });
  });

  it("returns null when the flow has no create data", async () => {
    const flow = enrolFlow({ publicKey: {} });
    flow.ui.nodes = [];
    expect(await passkeyEnrolFields(flow)).toBeNull();
  });
});

describe("webauthn 2FA (security keys, AAL2)", () => {
  it("detects the login trigger and enrol trigger nodes", () => {
    const login = nodeFlow("webauthn", "webauthn_login_trigger", JSON.stringify({ publicKey: { challenge: "AQID" } }), "button");
    expect(flowHasWebauthn2faLogin(login)).toBe(true);
    expect(flowHasWebauthnEnrol(login)).toBe(false);
    const enrol = nodeFlow("webauthn", "webauthn_register_trigger", JSON.stringify({ publicKey: { user: { id: "AQID" }, challenge: "AQID" } }), "button");
    expect(flowHasWebauthnEnrol(enrol)).toBe(true);
    expect(flowHasWebauthn2faLogin(enrol)).toBe(false);
  });

  it("runs the assertion from webauthn_login_trigger and returns webauthn_login", async () => {
    const get = vi.fn().mockResolvedValue({
      id: "wk",
      type: "public-key",
      rawId: u8(1),
      response: {
        authenticatorData: u8(2),
        clientDataJSON: u8(3),
        signature: u8(4),
        userHandle: null,
      },
    });
    vi.stubGlobal("navigator", { credentials: { get, create: vi.fn() } });

    const flow = nodeFlow(
      "webauthn",
      "webauthn_login_trigger",
      JSON.stringify({ publicKey: { challenge: "AQID", allowCredentials: [{ id: "AQID", type: "public-key" }] } }),
      "button",
    );
    // The AAL2 flow always carries the identifier node (pre-filled username);
    // Kratos rejects the submit without it ("Property identifier is missing").
    flow.ui.nodes.push({
      type: "input",
      group: "default",
      attributes: { name: "identifier", type: "hidden", value: "alice" },
    });
    const out = await webauthn2faLoginFields(flow);
    expect(out).not.toBeNull();
    expect(out!.identifier).toBe("alice"); // must forward the identifier
    // allowCredentials id must be decoded before get.
    expect(Array.from(new Uint8Array(get.mock.calls[0][0].publicKey.allowCredentials[0].id))).toEqual([1, 2, 3]);
    const body = JSON.parse(out!.webauthn_login);
    expect(body.id).toBe("wk");
    expect(body.response.signature).toBe(bufferEncode(u8(4)));
    expect(body.response.userHandle).toBe(""); // null → empty
    expect(await webauthn2faLoginFields(nodeFlow("webauthn", "other", "{}"))).toBeNull();
  });

  it("runs registration and carries the display name", async () => {
    const create = vi.fn().mockResolvedValue({
      id: "wk-new",
      type: "public-key",
      rawId: u8(9),
      response: { attestationObject: u8(1), clientDataJSON: u8(2) },
    });
    vi.stubGlobal("navigator", { credentials: { get: vi.fn(), create } });

    const flow = nodeFlow(
      "webauthn",
      "webauthn_register_trigger",
      JSON.stringify({ publicKey: { user: { id: "AQID", name: "a", displayName: "a" }, challenge: "BAUG" } }),
      "button",
    );
    const out = await webauthnEnrolFields(flow, "YubiKey 5C");
    expect(out).not.toBeNull();
    expect(out!.webauthn_register_displayname).toBe("YubiKey 5C");
    expect(Array.from(new Uint8Array(create.mock.calls[0][0].publicKey.challenge))).toEqual([4, 5, 6]);
    const body = JSON.parse(out!.webauthn_register);
    expect(body.id).toBe("wk-new");
    expect(body.response.attestationObject).toBe(bufferEncode(u8(1)));
    expect(await webauthnEnrolFields(nodeFlow("webauthn", "other", "{}"), "x")).toBeNull();
  });
});
