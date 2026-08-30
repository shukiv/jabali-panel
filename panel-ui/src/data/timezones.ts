// Shared IANA/PHP timezone list. Reused by the admin Server Settings selector and
// the per-domain PHP Settings selector so the options are identical in both places
// (GH #1332). `Intl.supportedValuesOf("timeZone")` is the full IANA tz database —
// the same set PHP validates a `date.timezone` value against — so it includes
// zones the old curated list missed (e.g. Europe/Bucharest). Guarded with a tiny
// fallback for the rare engine without `supportedValuesOf`.
export const IANA_TIMEZONES: string[] = (() => {
  try {
    return Array.from(Intl.supportedValuesOf("timeZone"));
  } catch {
    return ["UTC"];
  }
})();
