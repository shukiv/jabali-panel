// kratos.ts — thin typed wrapper around the Kratos browser self-service API.
//
// nginx proxies /.ory/* to Kratos's public port (4433), same origin as the
// SPA, so browsers attach the ory_kratos_session cookie automatically.
// We never use the Admin API from the SPA — that's always server-side via
// panel-api.

import axios, { type AxiosError } from "axios";

const KRATOS_BASE = "/.ory";

export const kratosClient = axios.create({
  baseURL: KRATOS_BASE,
  withCredentials: true,
  timeout: 15000,
  headers: { Accept: "application/json" },
});

// ---------------------------------------------------------------------------
// Flow types — minimal shape of what we actually read. Kratos responses have
// many more fields; we intentionally keep the surface small so the renderer
// doesn't overfit to upstream cosmetic changes.
// ---------------------------------------------------------------------------

export type KratosNodeInputAttributes = {
  name: string;
  type: "text" | "password" | "hidden" | "submit" | "email" | "checkbox" | "tel" | "number" | "button";
  value?: string | number | boolean | null;
  required?: boolean;
  disabled?: boolean;
  autocomplete?: string;
  pattern?: string;
};

export type KratosMessage = {
  id: number;
  text: string;
  type: "info" | "error" | "success";
  context?: Record<string, unknown>;
};

export type KratosNode = {
  type: "input" | "img" | "a" | "text" | "script";
  group: string; // "default" | "password" | "totp" | "lookup_secret" | "webauthn" | ...
  attributes: KratosNodeInputAttributes;
  meta?: { label?: { text?: string; id?: number } };
  messages?: KratosMessage[];
};

export type KratosFlow = {
  id: string;
  type: "browser" | "api";
  expires_at: string;
  issued_at: string;
  request_url: string;
  ui: {
    action: string;
    method: "POST" | "GET";
    nodes: KratosNode[];
    messages?: KratosMessage[];
  };
  // Login flows optionally advertise the authenticator-assurance level they
  // require. When we've finished AAL1 (password) and AAL2 is required, Kratos
  // sets `requested_aal: "aal2"` and the UI switches to TOTP / backup-code
  // inputs on the next fetch.
  requested_aal?: "aal1" | "aal2";
  refresh?: boolean;
};

// ---------------------------------------------------------------------------
// API calls
// ---------------------------------------------------------------------------

/**
 * Initialise a login flow. Kratos issues a CSRF token and builds the UI node
 * tree for whichever credential method(s) the identity schema allows. On the
 * browser endpoint, GET redirects to our UI by default — we set
 * `Accept: application/json` to receive the flow object directly instead.
 */
export async function initLoginFlow(): Promise<KratosFlow> {
  try {
    const resp = await kratosClient.get<KratosFlow>("/self-service/login/browser");
    return resp.data;
  } catch (err) {
    const ax = err as AxiosError<{
      error?: { id?: string };
      redirect_browser_to?: string;
    }>;
    const errorId = ax.response?.data?.error?.id;
    // Kratos refuses to mint a fresh login flow when an aal1 session
    // already exists (after TOTP enrolment, panel-api's whoami starts
    // 401-ing so the SPA bounces to /login but Kratos sees the aal1
    // cookie). It returns 422 with redirect_browser_to=...?aal=aal2.
    //
    // Doing the aal2 GET via XHR ourselves was breaking the per-flow
    // CSRF cookie — Kratos sets it via the 303 it emits on browser-
    // path navigations, and following with an XHR fetch left the
    // browser holding a cookie keyed to the wrong flow id. Use a
    // native window.location redirect instead so the browser handles
    // the cookie hand-off correctly.
    if (
      ax.response?.status === 422 ||
      errorId === "session_already_available" ||
      errorId === "browser_location_change_required"
    ) {
      const target =
        ax.response?.data?.redirect_browser_to ??
        "/.ory/self-service/login/browser?aal=aal2";
      window.location.assign(target);
      // The current page is being torn down; throw a sentinel so the
      // caller's await never resolves (UI shows the spinner).
      return new Promise<KratosFlow>(() => {});
    }
    throw err;
  }
}

/**
 * Re-fetch a flow by id. Used to rehydrate the form after a reload, or to
 * read the AAL2 nodes after Kratos upgrades the flow in response to password
 * success against a 2FA-enabled identity.
 */
export async function getLoginFlow(id: string): Promise<KratosFlow> {
  const resp = await kratosClient.get<KratosFlow>(
    `/self-service/login/flows?id=${encodeURIComponent(id)}`,
  );
  return resp.data;
}

/**
 * Submit a login flow. `body` MUST include csrf_token (copied from the flow's
 * hidden input) plus the credential fields the current AAL expects:
 *   - password: method=password, identifier, password
 *   - totp:     method=totp, totp_code
 *   - lookup_secret: method=lookup_secret, lookup_secret
 *
 * Kratos returns:
 *   200 with an updated flow when more input is needed (e.g. AAL2 step).
 *   200 with {session, session_token?} when login is complete.
 *   302/303 when the browser should follow a return_to redirect.
 */
export async function submitLoginFlow(
  flow: KratosFlow,
  body: Record<string, string | number | boolean>,
): Promise<KratosSubmitResult> {
  try {
    const resp = await kratosClient.post<KratosFlow | KratosSuccess>(flow.ui.action, body);
    // Success response contains a `session` object — if present, we're in.
    const data = resp.data as Partial<KratosSuccess> & Partial<KratosFlow>;
    if (data.session) {
      return { kind: "success", session: data.session };
    }
    // Otherwise Kratos returned an updated flow (likely AAL2 step required).
    if (data.ui) {
      return { kind: "continue", flow: data as KratosFlow };
    }
    return { kind: "error", message: "Unexpected response from identity provider" };
  } catch (err) {
    const ax = err as AxiosError<
      KratosFlow & {
        redirect_browser_to?: string;
        error?: { id?: string };
      }
    >;
    // 400 on a login flow with errors embedded in the flow's
    // ui.messages is the normal "wrong password" / "rate limited"
    // path — surface the flow so the caller can re-render.
    if (
      (ax.response?.status === 400 || ax.response?.status === 422) &&
      ax.response.data?.ui
    ) {
      return { kind: "continue", flow: ax.response.data };
    }
    // 422 is Kratos's "browser must redirect" signal — body:
    //   { error: { id: "browser_location_change_required" },
    //     redirect_browser_to: "...login/browser?aal=aal2" or
    //                          "...login?flow=<id>&aal=aal2" }
    //
    // Doing the redirect via XHR breaks the per-flow csrf cookie:
    // Kratos's CSRF protection ties body.csrf_token to a cookie keyed
    // by the flow id, set by Kratos's 303. Following the redirect
    // ourselves with an XHR sometimes leaves the browser holding a
    // stale per-flow cookie that no longer matches the new flow's
    // body token (CSRF 403 on the next POST). The native browser
    // redirect path is the only one Kratos-the-OAuth-y reliably
    // supports — so do that.
    if (ax.response?.status === 422) {
      const redirect = ax.response.data?.redirect_browser_to;
      if (redirect) {
        return { kind: "redirect", url: redirect };
      }
    }
    return { kind: "error", message: humanizeKratosError(ax) };
  }
}

export type KratosSession = {
  id: string;
  active: boolean;
  identity: {
    id: string;
    traits: { email: string; username?: string; is_admin?: boolean };
  };
};

type KratosSuccess = {
  session: KratosSession;
  session_token?: string;
};

export type KratosSubmitResult =
  | { kind: "success"; session: KratosSession }
  | { kind: "continue"; flow: KratosFlow }
  | { kind: "redirect"; url: string }
  | { kind: "error"; message: string };

/**
 * Who am I? Returns null when there's no active session — we distinguish
 * "not logged in" (401) from transient upstream errors (5xx / network)
 * so authProvider.check() can route to /login cleanly on the former and
 * surface a retry toast on the latter.
 */
export async function whoami(): Promise<KratosSession | null> {
  try {
    const resp = await kratosClient.get<KratosSession>("/sessions/whoami");
    return resp.data;
  } catch (err) {
    const ax = err as AxiosError;
    if (ax.response?.status === 401) return null;
    // For 5xx/network we re-throw so the caller can show a transient toast
    // rather than a silent logout on a Kratos blip.
    throw err;
  }
}

/**
 * Kick off the browser logout. Kratos returns a token + URL; the caller
 * issues a POST to the URL with the token to invalidate the session.
 * We wrap it into a single call that returns once the cookie is cleared.
 */
export async function logoutBrowser(): Promise<string> {
  const resp = await kratosClient.get<{ logout_token: string; logout_url: string }>(
    "/self-service/logout/browser",
  );
  // Return the logout_url for the caller to TOP-LEVEL navigate to (#255).
  // Browser-flow logout must be a real navigation, not an XHR: Kratos clears
  // the ory_kratos_session cookie via Set-Cookie on logout_url and 303s to the
  // configured return URL (/login). An XHR GET did not reliably clear the
  // cookie, so the session survived and /login bounced it straight back in.
  // logout_url already carries the token as a query param.
  return resp.data.logout_url;
}

/**
 * Re-fetch a settings flow by id. Settings is the post-login flow that
 * lets a user change their password and manage TOTP — Kratos owns the
 * flow object, we just render the nodes inline on the profile page.
 *
 * Same shape as login's getLoginFlow; separate endpoint because Kratos
 * scopes flow ids by flow type.
 */
export async function getSettingsFlow(id: string): Promise<KratosFlow> {
  const resp = await kratosClient.get<KratosFlow>(
    `/self-service/settings/flows?id=${encodeURIComponent(id)}`,
  );
  return resp.data;
}

/**
 * Result of an initSettingsFlow call. Kratos returns the flow on
 * success, asks for a privileged-session refresh on stale sessions,
 * or 401 when there's no session at all.
 */
export type SettingsInitResult =
  | { kind: "flow"; flow: KratosFlow }
  | { kind: "refresh_required" }
  | { kind: "unauthenticated" }
  | { kind: "error"; message: string };

/**
 * Initialise a settings flow without the redirect dance. Kratos's
 * /self-service/settings/browser endpoint normally 303s to the
 * configured ui_url; with `Accept: application/json` it returns the
 * flow JSON directly. The browser flow init is the only Kratos path
 * that performs the privileged-session check, so we still need it
 * for that side-effect — we just sidestep the page reload.
 *
 * Privileged-session expired → 403 with id "session_refresh_required".
 * No session at all → 401.
 */
export async function initSettingsFlow(): Promise<SettingsInitResult> {
  try {
    // kratosClient already sets Accept: application/json globally —
    // Kratos returns 200 + flow body instead of 303 to ui_url. No
    // need to disable axios follow-redirects (doing so would break
    // the cookie-only flow path the SPA uses everywhere else).
    const resp = await kratosClient.get<KratosFlow>(
      "/self-service/settings/browser",
    );
    return { kind: "flow", flow: resp.data };
  } catch (err) {
    const ax = err as AxiosError<{
      error?: { id?: string };
      redirect_browser_to?: string;
    }>;
    const status = ax.response?.status;
    const errorId = ax.response?.data?.error?.id;
    if (status === 403 && errorId === "session_refresh_required") {
      return { kind: "refresh_required" };
    }
    if (status === 401) {
      return { kind: "unauthenticated" };
    }
    if (status === 422 || errorId === "browser_location_change_required") {
      const target =
        ax.response?.data?.redirect_browser_to ??
        "/.ory/self-service/settings/browser";
      window.location.assign(target);
      return new Promise<SettingsInitResult>(() => {});
    }
    return { kind: "error", message: humanizeKratosError(ax) };
  }
}

/**
 * Submit a settings flow update (e.g. password change, TOTP enrolment).
 * Kratos returns:
 *   200 with the updated flow — UI re-renders with success/error in
 *     ui.messages and per-node errors. The flow stays alive so the user
 *     can fix mistakes without re-initialising.
 *   401 when the privileged session has expired — Kratos redirects to
 *     login, we surface that as an error so the caller can prompt
 *     re-authentication.
 *   403 / 422 with a flow body on validation errors — same shape as 200.
 */
export async function submitSettingsFlow(
  flow: KratosFlow,
  body: Record<string, string | number | boolean>,
): Promise<KratosSubmitResult> {
  try {
    const resp = await kratosClient.post<KratosFlow>(flow.ui.action, body);
    if (resp.data?.ui) {
      return { kind: "continue", flow: resp.data };
    }
    return { kind: "error", message: "Unexpected response from identity provider" };
  } catch (err) {
    const ax = err as AxiosError<KratosFlow>;
    // 400 / 422 with a flow body is the normal "field validation
    // failed" / "csrf_token mismatch" path — surface the flow so the
    // UI can re-render with the per-field errors.
    if (
      (ax.response?.status === 400 || ax.response?.status === 422) &&
      ax.response.data?.ui
    ) {
      return { kind: "continue", flow: ax.response.data };
    }
    if (ax.response?.status === 401 || ax.response?.status === 403) {
      return {
        kind: "error",
        message: "Your session needs re-authentication. Sign out and back in to manage account security.",
      };
    }
    return { kind: "error", message: humanizeKratosError(ax) };
  }
}

function humanizeKratosError(err: AxiosError): string {
  const status = err.response?.status;
  if (!status) return "Network error — could not reach identity service";
  if (status === 429) return "Too many attempts — try again in a minute";
  if (status >= 500) return "Identity service temporarily unavailable";
  return err.message ?? "Login failed";
}

/**
 * JAB-232 (ADR-0165 addendum): redeem a billing-SSO login link.
 *
 * The panel-api mints a URL like `/recovery?flow=<id>&code=<code>`. Kratos's
 * code recovery flow carries NO CSRF node, so a same-origin POST of the code
 * creates a session for the TARGET identity. On success Kratos answers
 * `422 browser_location_change_required` — the session cookie is already set on
 * that response. We deliberately do NOT follow its `/settings?flow=` target
 * (that is Kratos's optional "now set a new password" step); for an SSO login
 * we only need the user authenticated, so the caller lands them on their shell
 * home instead. Any other outcome (2xx flow re-render, 400/410/422 without the
 * location-change signal) means the code did not redeem — expired or reused.
 */
export type RecoveryRedeemResult =
  | { kind: "ok" }
  | { kind: "error"; message: string };

export async function redeemRecoveryCode(
  flow: string,
  code: string,
): Promise<RecoveryRedeemResult> {
  if (!flow || !code) {
    return { kind: "error", message: "This login link is missing its token." };
  }
  // JAB-274: trust the SESSION, not the redemption's HTTP status. A single
  // login link is routinely submitted more than once for the same browser —
  // React StrictMode double-invokes the effect, and prefetch / link-preview
  // agents can fire an extra request. The first submit sets the session (Kratos
  // answers 422 browser_location_change_required); a second submit of the
  // now-redeemed code, with a live session, comes back as a 303 that axios
  // follows transparently into a 2xx. That 2xx used to be reported as
  // "invalid or already used" even though the user was signed in. So on any
  // non-blcr outcome we ask Kratos whether a session exists before failing.
  try {
    await kratosClient.post(
      `/self-service/recovery?flow=${encodeURIComponent(flow)}`,
      { method: "code", code },
    );
    // 2xx is ambiguous: a re-rendered flow (code rejected) OR a
    // transparently-followed success redirect from a duplicate submit.
    return okIfSignedIn("This login link is invalid or has already been used.");
  } catch (err) {
    const ax = err as AxiosError<{
      error?: { id?: string };
      redirect_browser_to?: string;
    }>;
    const status = ax.response?.status;
    const errorId = ax.response?.data?.error?.id;
    if (status === 422 && errorId === "browser_location_change_required") {
      return { kind: "ok" };
    }
    if (status === 410) {
      return {
        kind: "error",
        message: "This login link has expired — ask for a new one.",
      };
    }
    if (status === 400 || status === 422) {
      return okIfSignedIn("This login link is invalid or has already been used.");
    }
    // Network / 5xx — a prior submit may still have signed us in.
    return okIfSignedIn(humanizeKratosError(ax));
  }
}

// okIfSignedIn resolves to success when a Kratos session already exists (a
// prior submit of the same link established it), otherwise the supplied error.
// JAB-274.
async function okIfSignedIn(
  errorMessage: string,
): Promise<RecoveryRedeemResult> {
  try {
    if (await whoami()) return { kind: "ok" };
  } catch {
    // whoami itself failed (Kratos blip) — surface the error below rather than
    // masking a genuine failure as success.
  }
  return { kind: "error", message: errorMessage };
}

// ---------------------------------------------------------------------------
// Renderer helpers — project flow.ui.nodes to a flat shape the React form
// can render without caring about Kratos's internal taxonomy.
// ---------------------------------------------------------------------------

export type RenderableField = {
  name: string;
  kind: "text" | "email" | "password" | "tel" | "number" | "hidden" | "submit";
  value: string;
  label?: string;
  required: boolean;
  disabled: boolean;
  autocomplete?: string;
  group: string;
  errors: string[];
};

/**
 * Extract the visible + hidden fields from a flow for a specific group
 * (typically "password" first, then "totp" after the AAL2 step). The
 * "default" group is always included because it carries the CSRF token
 * and the flow's method + action metadata.
 */
export function renderableFields(flow: KratosFlow, group: string): RenderableField[] {
  const out: RenderableField[] = [];
  for (const node of flow.ui.nodes) {
    if (node.type !== "input") continue;
    if (node.group !== "default" && node.group !== group) continue;
    const attrs = node.attributes;
    const type = attrs.type;
    if (type === "checkbox" || type === "button") {
      // Kratos sometimes emits non-input types we don't render directly.
      continue;
    }
    const value = attrs.value === undefined || attrs.value === null ? "" : String(attrs.value);
    out.push({
      name: attrs.name,
      kind: type,
      value,
      label: node.meta?.label?.text,
      required: !!attrs.required,
      disabled: !!attrs.disabled,
      autocomplete: attrs.autocomplete,
      group: node.group,
      errors: (node.messages ?? []).filter((m) => m.type === "error").map((m) => m.text),
    });
  }
  return out;
}

/**
 * TOTP enrolment surfaces a QR code image and the base32 secret as
 * non-input nodes. Pull them out so the form can render them above
 * the verification-code field. Returns null when the flow doesn't
 * carry an enrolment payload (already-enrolled users, or non-TOTP
 * flows).
 */
export function totpEnrolmentDisplay(
  flow: KratosFlow,
): { qrSrc?: string; secret?: string } | null {
  let qrSrc: string | undefined;
  let secret: string | undefined;
  for (const node of flow.ui.nodes) {
    if (node.group !== "totp") continue;
    // img + text nodes don't carry an `attributes.name` field — only
    // input nodes do. Filter by node.type + (id|node_type) instead;
    // Kratos returns at most one img + one secret-text per totp
    // group during enrolment, so type alone is enough.
    if (node.type === "img") {
      const attrs = node.attributes as unknown as { src?: string };
      qrSrc = attrs.src ?? qrSrc;
    }
    if (node.type === "text") {
      const attrs = node.attributes as unknown as {
        text?: { text?: string; id?: number };
        id?: string;
      };
      // The base32 secret text node has id "totp_secret_key" or text.id
      // 1050003 in v26. Match either to stay robust across upgrades.
      const looksLikeSecret =
        attrs.id === "totp_secret_key" ||
        attrs.text?.id === 1050003 ||
        // Fallback heuristic: a base32 secret is uppercase A-Z + 2-7
        // and 16+ chars long. Catches version drift without coupling
        // to internal Kratos node ids.
        (typeof attrs.text?.text === "string" &&
          /^[A-Z2-7]{16,}$/.test(attrs.text.text));
      if (looksLikeSecret) {
        secret = attrs.text?.text ?? secret;
      }
    }
  }
  if (!qrSrc && !secret) return null;
  return { qrSrc, secret };
}

/**
 * After regenerating recovery codes, Kratos surfaces the new codes as
 * a `text` node in the `lookup_secret` group. Returns the codes split
 * to an array, or null if the flow doesn't carry them (already-set
 * state or unrelated flow).
 */
export function lookupSecretReveal(flow: KratosFlow): string[] | null {
  for (const node of flow.ui.nodes) {
    if (node.group !== "lookup_secret") continue;
    if (node.type !== "text") continue;
    const attrs = node.attributes as unknown as {
      text?: {
        text?: string;
        id?: number;
        context?: { secrets?: { text?: string }[] };
      };
      id?: string;
    };
    // Match the lookup-secret reveal text by Kratos id 1050015 or
    // attribute id, falling back to "any text node in the
    // lookup_secret group whose context carries a secrets array".
    const looksLikeReveal =
      attrs.id === "lookup_secret_codes" ||
      attrs.text?.id === 1050015 ||
      Array.isArray(attrs.text?.context?.secrets);
    if (!looksLikeReveal) continue;
    const ctx = attrs.text?.context?.secrets;
    if (Array.isArray(ctx)) {
      return ctx.map((s) => s.text ?? "").filter(Boolean);
    }
    if (attrs.text?.text) {
      return attrs.text.text.split(/\s+/).filter(Boolean);
    }
  }
  return null;
}

/**
 * Flat list of top-level flow messages (not per-field). Kratos uses these
 * for cross-field errors like "invalid csrf token" or "account locked".
 */
export function flowMessages(flow: KratosFlow): string[] {
  return (flow.ui.messages ?? []).filter((m) => m.type === "error").map((m) => m.text);
}

/**
 * Pull the csrf_token hidden input's value out of the flow. Missing means
 * this is an API flow (we only use browser flows, so this should never
 * be missing in practice — but callers should tolerate empty gracefully).
 */
export function csrfToken(flow: KratosFlow): string {
  for (const node of flow.ui.nodes) {
    if (node.type === "input" && node.attributes.name === "csrf_token") {
      return String(node.attributes.value ?? "");
    }
  }
  return "";
}
