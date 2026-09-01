// loginHostHint — decides whether the login page should warn that the
// panel is being reached at the wrong host (GH #1411).
//
// Kratos is same-origin: login cookies, CSRF tokens and allowed return
// URLs are all bound to the panel's configured hostname. Reaching the
// panel over the raw server IP (or a stale FQDN after a rename) can't
// complete the flow — the identity fetch fails and, because kratos.ts's
// axios client has its own 15s timeout, it hangs first, which is the
// "loads very slowly" the reporter saw.
//
// We only know it's the *wrong* host once the public /branding response
// tells us the configured hostname. When it does and it differs from the
// current host, we offer a one-click link to the right host. When the
// configured hostname is unknown (branding not yet loaded, or an IP-only
// install that never set one) we fall back to the older heuristic: warn
// on a bare-IP host, but ONLY after branding settled so an IP-configured
// panel (where login genuinely works) doesn't flash the banner on every
// load before the fetch returns.

export type LoginHostHint =
  | { show: false }
  | { show: true; kind: "mismatch"; hostname: string; targetHref: string }
  | { show: true; kind: "ip" };

const IP_HOST_RE = /^\d{1,3}(\.\d{1,3}){3}$/;

function isBareIPHost(hostname: string): boolean {
  // Dotted-quad IPv4, or anything carrying a ":" (an IPv6 literal, or a
  // host:port form) — none of which match a normal FQDN.
  return IP_HOST_RE.test(hostname) || hostname.includes(":");
}

// Rewrite the current URL to the configured host, preserving scheme,
// port, path and query — except the Kratos `flow` id, which is bound to
// the origin that minted it; carrying it to another origin rehydrates a
// foreign flow and errors. Returns "" if the href can't be parsed.
function hrefForHost(currentHref: string, hostname: string): string {
  try {
    const u = new URL(currentHref);
    u.hostname = hostname;
    u.searchParams.delete("flow");
    return u.toString();
  } catch {
    return "";
  }
}

export function loginHostHint(args: {
  // Configured hostname from /branding. `undefined` = branding not
  // settled yet; "" = settled but no hostname configured.
  configuredHostname: string | undefined;
  brandingSettled: boolean;
  currentHostname: string;
  currentHref: string;
}): LoginHostHint {
  const current = args.currentHostname.trim().toLowerCase();
  const configured = (args.configuredHostname ?? "").trim().toLowerCase();

  if (configured) {
    // Known configured host: warn only on a real mismatch. When it
    // matches — including an IP-only install reached at its own IP —
    // login works, so stay silent.
    if (configured !== current) {
      const targetHref = hrefForHost(args.currentHref, configured);
      if (targetHref) {
        return { show: true, kind: "mismatch", hostname: configured, targetHref };
      }
    }
    return { show: false };
  }

  // Configured host unknown. Only fall back to the IP heuristic once
  // branding has settled, so we don't flicker before it loads.
  if (args.brandingSettled && isBareIPHost(current)) {
    return { show: true, kind: "ip" };
  }
  return { show: false };
}
