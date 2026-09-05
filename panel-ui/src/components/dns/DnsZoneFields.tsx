// DnsZoneFields — the shared body of the "Add DNS Zone" form (GH #1540): the
// pointed apex IP plus a DNS Template (Default / Microsoft 365 / Google
// Workspace) and the template's optional helper inputs. Used by both the tenant
// drawer (UserDomainDrawer, dns mode) and the admin drawer (AdminDNSZoneDrawer),
// so the two lists offer the same simple form johnnyq asked for.
//
// It reads the surrounding <Form> via Form.useFormInstance(), so a parent just
// drops <DnsZoneFields publicIP={caps?.public_ipv4} publicIPv6={caps?.public_ipv6} />
// inside its <Form> — the field names (ip_address, ip6_address, mail_provider,
// m365_onmicrosoft, google_dkim) match the POST /domains body verbatim.
import { useEffect } from "react";
import { Form, Input, Select } from "antd";

// isIPv4 accepts a dotted-quad with every octet in 0–255. The backend
// (net.ParseIP + To4) is the authority; this only gives the user immediate,
// friendly validation and blocks an obvious typo before the round-trip.
const isIPv4 = (value: string): boolean => {
  const parts = value.trim().split(".");
  if (parts.length !== 4) return false;
  return parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) >= 0 && Number(p) <= 255);
};

// isIPv6 is deliberately permissive (hex + colons, must contain a colon) and
// rejects an IPv4 / IPv4-mapped value so the user puts those in the v4 field.
// The backend (net.ParseIP + To4()==nil) is the authority.
const isIPv6 = (value: string): boolean => {
  const v = value.trim();
  if (!v.includes(":")) return false;
  if (/^::ffff:\d+\.\d+\.\d+\.\d+$/i.test(v)) return false; // IPv4-mapped → use the v4 field
  return /^[0-9a-f:]+$/i.test(v);
};

export interface DnsZoneFieldsProps {
  // The panel's public IPv4, prefilled into the apex IP field so the common
  // case ("point it at this server") is one click. The user can overwrite it.
  publicIP?: string;
  // The panel's public IPv6, prefilled into the optional AAAA field. Empty when
  // the server has no IPv6 — the field then renders blank and stays optional.
  publicIPv6?: string;
}

export const DnsZoneFields = ({ publicIP, publicIPv6 }: DnsZoneFieldsProps) => {
  const form = Form.useFormInstance();
  const template = Form.useWatch("mail_provider", form) ?? "none";

  // Prefill the apex IPs with the panel's IPs once, and never clobber a value
  // the user already typed (or one restored on a remount). destroyOnClose
  // remounts the drawer per open, so this re-runs with an empty field each open.
  useEffect(() => {
    if (publicIP && !form.getFieldValue("ip_address")) {
      form.setFieldValue("ip_address", publicIP);
    }
    if (publicIPv6 && !form.getFieldValue("ip6_address")) {
      form.setFieldValue("ip6_address", publicIPv6);
    }
  }, [publicIP, publicIPv6, form]);

  return (
    <>
      <Form.Item
        label="IP Address"
        name="ip_address"
        // initialValue seeds the panel IP synchronously and, crucially, is what
        // form.resetFields() restores to — without it a parent drawer that calls
        // resetFields on open (UserDomainDrawer) would blank the field on reopen.
        // The effect below is the late-caps-load fallback (caps undefined at mount).
        initialValue={publicIP}
        tooltip="The IPv4 address this domain's apex (@) record points to. Prefilled with this server's IP — change it if the site lives elsewhere."
        rules={[
          { required: true, message: "IP address is required" },
          {
            validator: (_, v) =>
              !v || isIPv4(v)
                ? Promise.resolve()
                : Promise.reject(new Error("Enter a valid IPv4 address (e.g. 203.0.113.10)")),
          },
        ]}
      >
        <Input placeholder="e.g., 203.0.113.10" />
      </Form.Item>

      {/* GH #1540 follow-up: optional apex IPv6 (AAAA). Prefilled with the
          server's IPv6 when it has one; left blank (and optional) otherwise. */}
      <Form.Item
        label="IPv6 Address (optional)"
        name="ip6_address"
        initialValue={publicIPv6}
        tooltip="Optional — the IPv6 address for the apex AAAA record. Prefilled with this server's IPv6 if it has one. Leave blank for no AAAA."
        rules={[
          {
            validator: (_, v) =>
              !v || isIPv6(v)
                ? Promise.resolve()
                : Promise.reject(new Error("Enter a valid IPv6 address (e.g. 2001:db8::1)")),
          },
        ]}
      >
        <Input placeholder="e.g., 2001:db8::1" />
      </Form.Item>

      {/* GH #1540: "Template" — Default seeds no mail records (just the apex the
          user pointed above); Microsoft 365 / Google Workspace add that
          provider's mail DNS. Jabali Mail is intentionally absent — those
          records are set by adding a mail domain, which overrides them. Reuses
          the mail_provider field the backend already understands. */}
      <Form.Item
        label="Template"
        name="mail_provider"
        initialValue="none"
        tooltip="Default creates just the domain and its pointed IP. Microsoft 365 or Google Workspace also adds that provider's mail DNS records."
      >
        <Select
          options={[
            { value: "none", label: "Default (no mail records)" },
            { value: "m365", label: "Microsoft 365" },
            { value: "google", label: "Google Workspace" },
          ]}
        />
      </Form.Item>

      {template === "m365" && (
        <Form.Item
          label="Microsoft 365 tenant"
          name="m365_onmicrosoft"
          tooltip="Optional — your <tenant>.onmicrosoft.com. Adds the Microsoft 365 DKIM CNAMEs for this domain."
        >
          <Input placeholder="contoso.onmicrosoft.com (optional)" />
        </Form.Item>
      )}

      {template === "google" && (
        <Form.Item
          label="Google DKIM value"
          name="google_dkim"
          tooltip="Optional — paste the google._domainkey TXT value from the Google Admin console."
        >
          <Input.TextArea rows={2} placeholder="v=DKIM1; k=rsa; p=... (optional)" />
        </Form.Item>
      )}
    </>
  );
};
