// GH #1604 (johnnyq): the browser tab title was the static "Jabali Panel" from
// index.html, so an operator running several instances could not tell tabs
// apart. Prefix the address-bar host — "<hostname> | Jabali Panel" — so each
// instance's tab is identifiable at a glance. The host comes from
// window.location, not server settings, so it is correct before login and
// reflects exactly the address the tab was opened on (an IP shows as the IP).
//
// The suffix is branding-aware: when the operator has set a custom panel
// brand text (Look and feel), it reads "<host> | <brand> — Panel"; otherwise
// the default product name. This is the single builder both title writers go
// through — boot (setPageTitle in main.tsx) and the branding effect
// (useApplyBrandingToTitle) — so the branding hook can no longer overwrite the
// host away, which is what silently defeated #1604 before this.

const DEFAULT_SUFFIX = "Jabali Panel";

/**
 * buildPageTitle returns the document title for a given address-bar host and
 * optional custom brand text. An empty/whitespace host drops the prefix and
 * falls back to the suffix alone; an empty/whitespace brand uses the default
 * product name.
 */
export function buildPageTitle(
  hostname: string | null | undefined,
  brandText?: string | null,
): string {
  const host = (hostname ?? "").trim();
  const brand = (brandText ?? "").trim();
  const suffix = brand ? `${brand} — Panel` : DEFAULT_SUFFIX;
  return host ? `${host} | ${suffix}` : suffix;
}

/**
 * setPageTitle applies the host-only title at boot, before branding loads.
 * The branding effect later re-applies through buildPageTitle with the brand
 * text; both writes agree on the host, so there is no flash.
 */
export function setPageTitle(): void {
  document.title = buildPageTitle(window.location.hostname);
}
