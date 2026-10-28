// Shared IANA/PHP timezone list. Reused by the admin Server Settings selector and
// the per-domain PHP Settings selector so the options are identical in both places
// (GH #1332). `Intl.supportedValuesOf("timeZone")` is the full IANA tz database —
// the same set PHP validates a `date.timezone` value against — so it includes
// zones the old curated list missed (e.g. Europe/Bucharest). Guarded with a tiny
// fallback for the rare engine without `supportedValuesOf`.
//
// GH #2320: Chrome, Safari and Node return ICU's IDs, which keep old spellings
// (Asia/Calcutta, Europe/Kiev). Debian 13 and Ubuntu 24.04 ship those only in
// the optional tzdata-legacy package, so the server, timedatectl and PHP all
// refuse them. Each is listed by its current name instead, the alias's link
// target in tzdata.zi. internal/paneltime keeps the same map for API callers;
// a Go test keeps the two equal. The engines also leave out UTC, so it is added.
export const LEGACY_TIMEZONE_NAMES: Record<string, string> = {
  "Africa/Asmera": "Africa/Asmara",
  "America/Buenos_Aires": "America/Argentina/Buenos_Aires",
  "America/Catamarca": "America/Argentina/Catamarca",
  "America/Cordoba": "America/Argentina/Cordoba",
  "America/Godthab": "America/Nuuk",
  "America/Indianapolis": "America/Indiana/Indianapolis",
  "America/Jujuy": "America/Argentina/Jujuy",
  "America/Louisville": "America/Kentucky/Louisville",
  "America/Mendoza": "America/Argentina/Mendoza",
  "Asia/Calcutta": "Asia/Kolkata",
  "Asia/Katmandu": "Asia/Kathmandu",
  "Asia/Rangoon": "Asia/Yangon",
  "Asia/Saigon": "Asia/Ho_Chi_Minh",
  "Atlantic/Faeroe": "Atlantic/Faroe",
  "Europe/Kiev": "Europe/Kyiv",
  "Pacific/Enderbury": "Pacific/Kanton",
  "Pacific/Ponape": "Pacific/Pohnpei",
  "Pacific/Truk": "Pacific/Chuuk",
};

export const IANA_TIMEZONES: string[] = (() => {
  try {
    const names = Intl.supportedValuesOf("timeZone").map((z) => LEGACY_TIMEZONE_NAMES[z] ?? z);
    return Array.from(new Set([...names, "UTC"])).sort();
  } catch {
    return ["UTC"];
  }
})();
