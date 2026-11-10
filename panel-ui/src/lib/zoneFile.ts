import type { DomainEmailDNSHint } from "../hooks/useMailboxes";

// quoteTXT renders a TXT value as zone-file rdata: one quoted string, or
// several when it is longer than 255 characters, the most one string holds.
// The RSA DKIM key (GH #2475) is about 400. Mirrors dnscompile.QuoteTXT.
export function quoteTXT(value: string): string {
  const esc = (s: string) => s.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
  const parts: string[] = [];
  for (let i = 0; i < value.length || parts.length === 0; i += 255) {
    parts.push(`"${esc(value.slice(i, i + 255))}"`);
  }
  return parts.join(" ");
}

// zoneFileLine renders one hint as a bulk-import-friendly zone-file line. The
// value already carries record-data form (MX/SRV priorities inlined), so it
// drops in after "<name> IN <type> ". TXT rdata MUST be quoted for zone-file
// syntax — an unquoted "v=DMARC1; p=…" is truncated at the first ";" (a comment
// marker), silently corrupting DMARC/DKIM/TLS-RPT/SPF. Per-value copy stays bare
// (provider UIs want the raw string); only the zone-file form quotes.
export function zoneFileLine(h: DomainEmailDNSHint): string {
  const rdata = h.type === "TXT" && !h.value.startsWith('"') ? quoteTXT(h.value) : h.value;
  return `${h.name}\tIN\t${h.type}\t${rdata}`;
}
