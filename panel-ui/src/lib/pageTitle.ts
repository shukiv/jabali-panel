// GH #1604 (johnnyq): the browser tab title was the static "Jabali Panel" from
// index.html, so an operator running several instances could not tell tabs
// apart. Prefix the address-bar host — "<hostname> | Jabali Panel" — so each
// instance's tab is identifiable at a glance. The host comes from
// window.location, not server settings, so it is correct before login and
// reflects exactly the address the tab was opened on (an IP shows as the IP).

const SUFFIX = "Jabali Panel";

/**
 * buildPageTitle returns the document title for a given address-bar host.
 * An empty or whitespace-only host falls back to the bare product name.
 */
export function buildPageTitle(hostname: string | null | undefined): string {
  const host = (hostname ?? "").trim();
  return host ? `${host} | ${SUFFIX}` : SUFFIX;
}

/** setPageTitle applies buildPageTitle(window.location.hostname) to the tab. */
export function setPageTitle(): void {
  document.title = buildPageTitle(window.location.hostname);
}
