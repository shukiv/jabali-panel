// webauthn.ts — GH #917: passkey (passwordless WebAuthn) ceremony for the SPA.
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
// WITHOUT padding, and the exact field names Kratos's passkey strategy reads
// (`passkey_challenge` / `passkey_login` for login, `passkey_create_data` /
// `passkey_settings_register` for settings enrolment). Getting this wrong
// silently breaks authentication, so it is kept deliberately faithful and is
// covered by webauthn.test.ts.
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

// Read a Kratos node's input value by field name.
function nodeValue(flow: KratosFlow, name: string): string | undefined {
  for (const node of flow.ui.nodes) {
    if (node.type === "input" && node.attributes.name === name) {
      const v = node.attributes.value;
      return v == null ? undefined : String(v);
    }
  }
  return undefined;
}

// A login flow offers passkey when Kratos injected the discoverable-login
// challenge node; a settings flow offers enrolment via passkey_create_data.
export function flowHasPasskeyLogin(flow: KratosFlow): boolean {
  return nodeValue(flow, "passkey_challenge") !== undefined;
}
export function flowHasPasskeyEnrol(flow: KratosFlow): boolean {
  return nodeValue(flow, "passkey_create_data") !== undefined;
}

// Kratos serialises the WebAuthn options as { publicKey: { challenge, ... } }
// with base64url-encoded binary fields. We decode the binary fields the
// authenticator needs as ArrayBuffers before calling navigator.credentials.
type PublicKeyOptions = {
  publicKey: PublicKeyCredentialRequestOptions &
    PublicKeyCredentialCreationOptions & {
      // Kratos ships these as base64url strings; we rewrite them to BufferSource.
      challenge: unknown;
      allowCredentials?: Array<{ id: unknown; type: string; transports?: string[] }>;
      excludeCredentials?: Array<{ id: unknown; type: string; transports?: string[] }>;
      user?: { id: unknown; name: string; displayName: string };
    };
};

function decodeId<T extends { id: unknown }>(c: T): T {
  return { ...c, id: bufferDecode(String(c.id)) };
}

// passkeyLoginFields runs the discoverable-login ceremony and returns the
// credential field to POST. `identifier` is empty: passkey login is
// discoverable — the authenticator supplies the user handle, so the user
// doesn't type a username. Returns null when the flow carries no passkey
// challenge. Throws when the ceremony fails (no authenticator, user cancel).
export async function passkeyLoginFields(
  flow: KratosFlow,
): Promise<{ passkey_login: string; identifier: string } | null> {
  const raw = nodeValue(flow, "passkey_challenge");
  if (raw === undefined) return null;

  const opt = JSON.parse(raw) as PublicKeyOptions;
  opt.publicKey.challenge = bufferDecode(String(opt.publicKey.challenge)) as BufferSource;
  if (opt.publicKey.allowCredentials) {
    opt.publicKey.allowCredentials = opt.publicKey.allowCredentials.map(decodeId);
  }

  const credential = (await navigator.credentials.get({
    publicKey: opt.publicKey as PublicKeyCredentialRequestOptions,
  })) as PublicKeyCredential | null;
  if (!credential) throw new Error("No passkey credential was returned");

  const response = credential.response as AuthenticatorAssertionResponse;
  const passkey_login = JSON.stringify({
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
  return { passkey_login, identifier: "" };
}

// passkeyEnrolFields runs the registration ceremony against the settings
// flow's passkey_create_data and returns the field to POST. Kratos has
// already set user.name/displayName from the identity's display-name trait
// (the username, per the identity schema), so no display-name input is
// needed. Returns null when the flow carries no create data. Throws on
// ceremony failure.
export async function passkeyEnrolFields(
  flow: KratosFlow,
): Promise<{ passkey_settings_register: string } | null> {
  const raw = nodeValue(flow, "passkey_create_data");
  if (raw === undefined) return null;

  const opt = JSON.parse(raw) as PublicKeyOptions;
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
  if (!credential) throw new Error("No passkey credential was created");

  const response = credential.response as AuthenticatorAttestationResponse;
  const passkey_settings_register = JSON.stringify({
    id: credential.id,
    rawId: bufferEncode(credential.rawId),
    type: credential.type,
    response: {
      attestationObject: bufferEncode(response.attestationObject),
      clientDataJSON: bufferEncode(response.clientDataJSON),
    },
  });
  return { passkey_settings_register };
}
