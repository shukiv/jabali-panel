// DnsZoneFields — the shared body of the "Add DNS Zone" form (GH #1540): the
// pointed apex IP plus a DNS Template (Default / Microsoft 365 / Google
// Workspace) and the template's optional helper inputs. Used by both the tenant
// drawer (UserDomainDrawer, dns mode) and the admin drawer (AdminDNSZoneDrawer),
// so the two lists offer the same simple form johnnyq asked for.
//
// It reads the surrounding <Form> via Form.useFormInstance(), so a parent just
// drops <DnsZoneFields publicIP={caps?.public_ipv4} /> inside its <Form> — the
// field names (ip_address, mail_provider, m365_onmicrosoft, google_dkim) match
// the POST /domains body verbatim.
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

export interface DnsZoneFieldsProps {
  // The panel's public IPv4, prefilled into the apex IP field so the common
  // case ("point it at this server") is one click. The user can overwrite it.
  publicIP?: string;
}

export const DnsZoneFields = ({ publicIP }: DnsZoneFieldsProps) => {
  const form = Form.useFormInstance();
  const template = Form.useWatch("mail_provider", form) ?? "none";

  // Prefill the apex IP with the panel's IP once, and never clobber a value the
  // user already typed (or one restored on a remount). destroyOnClose remounts
  // the drawer per open, so this re-runs with an empty field each open.
  useEffect(() => {
    if (publicIP && !form.getFieldValue("ip_address")) {
      form.setFieldValue("ip_address", publicIP);
    }
  }, [publicIP, form]);

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
