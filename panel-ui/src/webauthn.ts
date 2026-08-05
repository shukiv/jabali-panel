// webauthn.ts — GH #917: passkey (passwordless AAL1) and WebAuthn security-key
// (second-factor AAL2) ceremonies for the SPA.
//
// Kratos ships a browser helper (/.well-known/ory/webauthn.js) that runs the
// WebAuthn ceremony and submits a hidden HTML form. Our SPA renders its own
// AntD forms and talks to Kratos over XHR/JSON, so that script's
// form.submit() model doesn't fit. Instead we perform the ceremony directly
// with `navigator.credentials` and hand the resulting field back to the
// existing kratos.ts submit path (which adds csrf_token + method).
//
// The encode/decode + result JSON shape below MIRROR Kratos's own webauthn.js
// byte-for-byte (verified against the deployed v26.2.0 script): base64url
// WITHOUT padding, and the exact field names Kratos's strategies read.
//   passkey  (AAL1): passkey_challenge / passkey_login (login),
//                    passkey_create_data / passkey_settings_register (enrol).
//   webauthn (AAL2): webauthn_login_trigger / webauthn_login (login),
//                    webauthn_register_trigger / webauthn_register
//                    (+ webauthn_register_displayname) (enrol).
// Getting this wrong silently breaks authentication, so it is kept deliberately
// faithful and is covered by webauthn.test.ts.
import type { KratosFlow } from "./kratos";

// base64url (no padding) → bytes. Mirrors __oryWebAuthnBufferDecode. Returns a
// Uint8Array backed by a plain ArrayBuffer (not ArrayBufferLike) so it is
// directly assignable to WebAuthn's BufferSource fields.
export function bufferDecode(value: string): Uint8Array<ArrayBuffer> {
  let b64 = value.replaceAll("-", "+").replaceAll("_", "/");
  // Re-pad to a multiple of 4: Kratos emits unpadded base64url and relies on
  // the browser's lenient atob; padding here works in strict environments too.
  b64 += "=".repeat((4 - (b64.length % 4)) % 4);
  const bin = atob(b64);
  const buf = new ArrayBuffer(bin.length);
  const out = new Uint8Array(buf);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// bytes → base64url (no padding). Mirrors __oryWebAuthnBufferEncode.
export function bufferEncode(value: ArrayBuffer | null | undefined): string {
  if (!value) return "";
  const bytes = new Uint8Array(value);
  let bin = "";
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

// Is WebAuthn usable in this browser at all?
export function webauthnSupported(): boolean {
  return typeof window !== "undefined" && !!window.PublicKeyCredential;
}

// Read a Kratos node's input value by field name (works for hidden inputs and
// the button-typed *_trigger nodes, whose `value` carries the options JSON).
function nodeValue(flow: KratosFlow, name: string): string | undefined {
  for (const node of flow.ui.nodes) {
    if (node.type === "input" && node.attributes.name === name) {
      const v = node.attributes.value;
      return v == null ? undefined : String(v);
    }
  }
  return undefined;
}

// Kratos serialises the WebAuthn options as { publicKey: { challenge, ... } }
// with base64url-encoded binary fields. We decode the binary fields the
// authenticator needs as ArrayBuffers before calling navigator.credentials.
type PublicKeyOptions = {
  publicKey: PublicKeyCredentialRequestOptions &
    PublicKeyCredentialCreationOptions & {
      challenge: unknown;
      allowCredentials?: Array<{ id: unknown; type: string; transports?: string[] }>;
      excludeCredentials?: Array<{ id: unknown; type: string; transports?: string[] }>;
      user?: { id: unknown; name: string; displayName: string };
    };
};

function decodeId<T extends { id: unknown }>(c: T): T {
  return { ...c, id: bufferDecode(String(c.id)) };
}

// runGetCeremony: shared assertion (login) ceremony. Decodes the challenge +
// allowCredentials, calls navigator.credentials.get, and encodes the assertion
// into the exact JSON string Kratos's *_login fields expect.
async function runGetCeremony(rawOptions: string): Promise<string> {
  const opt = JSON.parse(rawOptions) as PublicKeyOptions;
  opt.publicKey.challenge = bufferDecode(String(opt.publicKey.challenge)) as BufferSource;
  if (opt.publicKey.allowCredentials) {
    opt.publicKey.allowCredentials = opt.publicKey.allowCredentials.map(decodeId);
  }
  const credential = (await navigator.credentials.get({
    publicKey: opt.publicKey as PublicKeyCredentialRequestOptions,
  })) as PublicKeyCredential | null;
  if (!credential) throw new Error("No credential was returned");
  const response = credential.response as AuthenticatorAssertionResponse;
  return JSON.stringify({
    id: credential.id,
    rawId: bufferEncode(credential.rawId),
    type: credential.type,
    response: {
      authenticatorData: bufferEncode(response.authenticatorData),
      clientDataJSON: bufferEncode(response.clientDataJSON),
      signature: bufferEncode(response.signature),
      userHandle: bufferEncode(response.userHandle),
    },
  });
}

// runCreateCeremony: shared attestation (registration) ceremony. Decodes
// user.id + challenge + excludeCredentials, calls navigator.credentials.create,
// and encodes the attestation into the JSON Kratos's *_register fields expect.
async function runCreateCeremony(rawOptions: string): Promise<string> {
  const opt = JSON.parse(rawOptions) as PublicKeyOptions;
  if (opt.publicKey.user) {
    opt.publicKey.user = { ...opt.publicKey.user, id: bufferDecode(String(opt.publicKey.user.id)) };
  }
  opt.publicKey.challenge = bufferDecode(String(opt.publicKey.challenge)) as BufferSource;
  if (opt.publicKey.excludeCredentials) {
    opt.publicKey.excludeCredentials = opt.publicKey.excludeCredentials.map(decodeId);
  }
  const credential = (await navigator.credentials.create({
    publicKey: opt.publicKey as PublicKeyCredentialCreationOptions,
  })) as PublicKeyCredential | null;
  if (!credential) throw new Error("No credential was created");
  const response = credential.response as AuthenticatorAttestationResponse;
  return JSON.stringify({
    id: credential.id,
    rawId: bufferEncode(credential.rawId),
    type: credential.type,
    response: {
      attestationObject: bufferEncode(response.attestationObject),
      clientDataJSON: bufferEncode(response.clientDataJSON),
    },
  });
}

// ---------------------------------------------------------------------------
// passkey (passwordless, AAL1)
// ---------------------------------------------------------------------------

export function flowHasPasskeyLogin(flow: KratosFlow): boolean {
  return nodeValue(flow, "passkey_challenge") !== undefined;
}
export function flowHasPasskeyEnrol(flow: KratosFlow): boolean {
  return nodeValue(flow, "passkey_create_data") !== undefined;
}

// passkeyLoginFields runs the discoverable-login ceremony. `identifier` is empty:
// passkey login is discoverable — the authenticator supplies the user handle, so
// the user doesn't type a username. Returns null when the flow carries no passkey
// challenge. Throws when the ceremony fails (no authenticator, user cancel).
export async function passkeyLoginFields(
  flow: KratosFlow,
): Promise<{ passkey_login: string; identifier: string } | null> {
  const raw = nodeValue(flow, "passkey_challenge");
  if (raw === undefined) return null;
  return { passkey_login: await runGetCeremony(raw), identifier: "" };
}

// passkeyEnrolFields runs registration against the settings flow's
// passkey_create_data. Kratos has already set user.name/displayName from the
// identity's display-name trait, so no display-name input is needed. Returns
// null when the flow carries no create data. Throws on ceremony failure.
export async function passkeyEnrolFields(
  flow: KratosFlow,
): Promise<{ passkey_settings_register: string } | null> {
  const raw = nodeValue(flow, "passkey_create_data");
  if (raw === undefined) return null;
  return { passkey_settings_register: await runCreateCeremony(raw) };
}

// ---------------------------------------------------------------------------
// webauthn (security key as a SECOND factor, AAL2)
// ---------------------------------------------------------------------------

export function flowHasWebauthn2faLogin(flow: KratosFlow): boolean {
  return nodeValue(flow, "webauthn_login_trigger") !== undefined;
}
export function flowHasWebauthnEnrol(flow: KratosFlow): boolean {
  return nodeValue(flow, "webauthn_register_trigger") !== undefined;
}

// webauthn2faLoginFields runs the AAL2 security-key assertion. The options live
// in the webauthn_login_trigger node's value (Kratos's own webauthn.js reads it
// the same way). The AAL2 login flow ALSO requires the `identifier` field —
// Kratos's webauthn.js submits the whole form, which carries the pre-filled
// identifier node; omitting it fails with "Property identifier is missing".
// Returns null when the flow carries no webauthn login trigger; throws on failure.
export async function webauthn2faLoginFields(
  flow: KratosFlow,
): Promise<{ webauthn_login: string; identifier: string } | null> {
  const raw = nodeValue(flow, "webauthn_login_trigger");
  if (raw === undefined) return null;
  return {
    webauthn_login: await runGetCeremony(raw),
    identifier: nodeValue(flow, "identifier") ?? "",
  };
}

// webauthnEnrolFields runs security-key registration. The create options live in
// the webauthn_register_trigger node's value; the user-chosen friendly name is
// submitted alongside as webauthn_register_displayname (Kratos stores it as the
// credential's label). Returns null when the flow carries no register trigger;
// throws on ceremony failure.
export async function webauthnEnrolFields(
  flow: KratosFlow,
  displayName: string,
): Promise<{ webauthn_register: string; webauthn_register_displayname: string } | null> {
  const raw = nodeValue(flow, "webauthn_register_trigger");
  if (raw === undefined) return null;
  return {
    webauthn_register: await runCreateCeremony(raw),
    webauthn_register_displayname: displayName,
  };
}
