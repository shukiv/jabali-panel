// useWebPushSubscription — encapsulates the VAPID-key fetch, service
// worker registration, and browser push subscription lifecycle. Mount
// once per shell (NotificationBell does this) and the hook manages
// all the navigator plumbing.
//
// On an unsupported browser (no ServiceWorker or PushManager) the hook
// reports `supported=false` and every action short-circuits. The
// NotificationBell handles the UI downgrade.
import { useCallback, useEffect, useMemo, useState } from "react";

import { apiClient } from "../apiClient";

// Service worker path. Matches the file under panel-ui/public.
const SW_PATH = "/sw-push.js";
const SW_SCOPE = "/";

export type PushPermission = NotificationPermission | "unsupported";

type VAPIDResponse = { public_key: string };

function base64UrlToUint8Array(base64url: string): Uint8Array {
  const padding = "=".repeat((4 - (base64url.length % 4)) % 4);
  const base64 = (base64url + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = window.atob(base64);
  const arr = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) arr[i] = raw.charCodeAt(i);
  return arr;
}

function ab2b64Url(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let s = "";
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
  return window.btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export interface WebPushState {
  supported: boolean;
  permission: PushPermission;
  subscribed: boolean;
  loading: boolean;
  subscribe: () => Promise<void>;
  unsubscribe: () => Promise<void>;
  // Service worker registration — exposed so consumers can use it
  // directly (e.g. for navigator.permissions query fallbacks).
  registration?: ServiceWorkerRegistration | null;
  // Last error from register / subscribe / unsubscribe — populated so
  // the UI can surface the actual SecurityError ("self-signed cert
  // blocks SW fetch", "user denied permission", etc.) instead of the
  // button silently going nowhere when something fails. null when the
  // last attempt succeeded or no attempt has been made.
  error: string | null;
}

export function useWebPushSubscription(): WebPushState {
  const supported = useMemo(() => {
    // Chrome refuses to register a ServiceWorker on an origin that
    // isn't a secure context — and an origin served with a self-signed
    // or cert-error TLS session is NOT a secure context even after the
    // user clicks through the warning page. Skip registration entirely
    // so the console doesn't fill with SecurityError stack traces and
    // the bell's downgrade UI kicks in cleanly.
    if (typeof window === "undefined") return false;
    if (!window.isSecureContext) return false;
    return (
      "serviceWorker" in navigator &&
      "PushManager" in window &&
      "Notification" in window
    );
  }, []);

  const [registration, setRegistration] = useState<ServiceWorkerRegistration | null>(null);
  const [subscribed, setSubscribed] = useState(false);
  const [permission, setPermission] = useState<PushPermission>(supported ? Notification.permission : "unsupported");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Register the service worker on first mount. Only once per tab;
  // navigator.serviceWorker.register is idempotent on the same URL.
  //
  // After register() we await navigator.serviceWorker.ready instead of
  // calling pushManager.getSubscription() on the freshly-returned
  // registration. register() resolves as soon as the SW file is fetched
  // and parsed, before the worker reaches the activated state — and on
  // Firefox in particular, querying the push manager during the
  // installing phase can return null even when a real subscription
  // exists from a previous session, which is the symptom we hit:
  // refresh flipped the bell back to "Enable" when it should stay
  // "Disable".
  useEffect(() => {
    if (!supported) return;
    let cancelled = false;
    (async () => {
      try {
        await navigator.serviceWorker.register(SW_PATH, { scope: SW_SCOPE, updateViaCache: "none" });
        const ready = await navigator.serviceWorker.ready;
        if (cancelled) return;
        setRegistration(ready);
        const existing = await ready.pushManager.getSubscription();
        if (cancelled) return;
        setSubscribed(existing !== null);
      } catch (err) {
        // Don't blow up the UI — bell just shows the disabled state.
        // SecurityError fires every page load on hostnames whose TLS
        // cert isn't trusted (.local with self-signed, etc.). It's
        // already surfaced via the subscribe button's "install LE cert"
        // hint, so log at info-level there to keep DevTools clean.
        const errName = (err as { name?: string } | null)?.name ?? "";
        if (errName === "SecurityError") {
          console.info("[webpush] service worker register skipped (cert not trusted)");
        } else {
          console.warn("[webpush] service worker register failed", err);
        }
        if (cancelled) return;
        setError(friendlyWebPushError(err, "register"));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [supported]);

  const subscribe = useCallback(async () => {
    if (!supported) {
      setError("This browser does not support Web Push.");
      return;
    }
    if (!registration) {
      // The most common case where supported=true but registration is null
      // is a self-signed TLS cert: window.isSecureContext returns true after
      // the user clicks through Chrome's warning, but the SW script fetch
      // still fails with SecurityError. Surface that explicitly so the
      // operator stops staring at a button that "does nothing".
      setError(
        "Service worker is not registered. This usually means the panel hostname is using a self-signed certificate; install a Let's Encrypt cert from SSL Manager → Panel SSL.",
      );
      return;
    }
    setError(null);
    setLoading(true);
    try {
      const perm = await Notification.requestPermission();
      setPermission(perm);
      if (perm !== "granted") {
        setError(
          perm === "denied"
            ? "Notifications are blocked. Allow them in your browser's site settings to enable browser push."
            : "Notification permission was not granted.",
        );
        return;
      }

      const { data } = await apiClient.get<VAPIDResponse>(
        "/notifications/webpush/vapid-public-key",
      );
      const serverKey = base64UrlToUint8Array(data.public_key);
      const sub = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: serverKey.buffer.slice(
          serverKey.byteOffset,
          serverKey.byteOffset + serverKey.byteLength,
        ) as ArrayBuffer,
      });
      const keys = sub.toJSON().keys ?? {};
      const p256dh = keys.p256dh ?? ab2b64Url(sub.getKey("p256dh") ?? new ArrayBuffer(0));
      const auth = keys.auth ?? ab2b64Url(sub.getKey("auth") ?? new ArrayBuffer(0));
      await apiClient.post("/notifications/webpush/subscribe", {
        endpoint: sub.endpoint,
        keys: { p256dh, auth },
        user_agent: navigator.userAgent,
      });
      setSubscribed(true);
    } catch (err) {
      console.warn("[webpush] subscribe failed", err);
      setError(friendlyWebPushError(err, "subscribe"));
    } finally {
      setLoading(false);
    }
  }, [registration, supported]);

  const unsubscribe = useCallback(async () => {
    if (!supported || !registration) return;
    setLoading(true);
    try {
      const sub = await registration.pushManager.getSubscription();
      if (!sub) {
        setSubscribed(false);
        return;
      }
      const endpoint = sub.endpoint;
      await sub.unsubscribe();
      await apiClient.delete("/notifications/webpush/subscribe", { data: { endpoint } });
      setSubscribed(false);
    } catch (err) {
      console.warn("[webpush] unsubscribe failed", err);
      setError(friendlyWebPushError(err, "unsubscribe"));
    } finally {
      setLoading(false);
    }
  }, [registration, supported]);

  return { supported, permission, subscribed, loading, subscribe, unsubscribe, registration, error };
}

// friendlyWebPushError maps a raw register/subscribe/unsubscribe failure
// to an operator-readable message. The most common SecurityError shape
// on Chrome ("Failed to register a ServiceWorker for scope ('…') with
// script ('…'): An SSL certificate error occurred when fetching the
// script.") gets translated into the actionable LE-cert hint. Anything
// else falls through to the raw message so dev cycles aren't blocked.
function friendlyWebPushError(err: unknown, op: "register" | "subscribe" | "unsubscribe"): string {
  const raw = err instanceof Error ? err.message : String(err);
  if (/SSL certificate|certificate error|InsecureContext/i.test(raw)) {
    return "The panel is served with an untrusted certificate, so the browser refuses to register the push service worker. Install a Let's Encrypt cert from SSL Manager → Panel SSL.";
  }
  if (/Permission denied|NotAllowedError/i.test(raw)) {
    return "Notifications are blocked. Allow them in your browser's site settings.";
  }
  if (/insecure context/i.test(raw)) {
    return "Browser push requires HTTPS with a trusted certificate. Install a Let's Encrypt cert from SSL Manager → Panel SSL.";
  }
  return `Could not ${op} for browser push: ${raw}`;
}
