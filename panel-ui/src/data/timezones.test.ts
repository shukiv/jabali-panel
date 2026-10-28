import { describe, it, expect } from "vitest";

import { IANA_TIMEZONES } from "./timezones";

// GH #2320: Chrome, Safari and Node list some zones by old ICU names that
// Debian 13 and Ubuntu 24.04 have no zone file for, so the panel refused them.
// Written out here, not imported, so the test checks the list itself.
const OLD_NAMES: Record<string, string> = {
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

describe("IANA_TIMEZONES", () => {
  it("lists each zone by the name the server's tz database has", () => {
    for (const [old, current] of Object.entries(OLD_NAMES)) {
      expect(IANA_TIMEZONES, old).not.toContain(old);
      expect(IANA_TIMEZONES, current).toContain(current);
    }
  });

  it("is sorted and lists each zone once", () => {
    expect(IANA_TIMEZONES).toEqual([...new Set(IANA_TIMEZONES)].sort());
    expect(IANA_TIMEZONES).toContain("UTC");
    expect(IANA_TIMEZONES).toContain("Europe/Berlin");
  });
});
