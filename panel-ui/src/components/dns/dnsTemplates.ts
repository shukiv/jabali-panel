// dnsTemplates — shared client bits for the tenant-facing custom DNS template
// picker (GH #1627). The picker reuses the existing "DNS Template" / "Template"
// mail_provider <Select> in the Add Web Domain and Add DNS Zone forms: the
// admin-defined templates are appended as a second option group, so a tenant
// picks a provider preset OR a custom template from one control.
//
// A custom template's option value is prefixed so the payload builder can tell
// it apart from a provider string without cross-referencing the list, and map it
// to dns_template_id (with mail_provider omitted — createDomainOp rejects a
// template alongside an explicit provider: template_and_provider_exclusive).
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "../../apiClient";

export const DNS_TEMPLATE_VALUE_PREFIX = "tmpl:";

export type DNSTemplateSummary = { id: string; name: string; description?: string };

// useDNSTemplates lists the admin-defined templates a tenant may select. The
// tenant route returns id/name/description only (no record bodies).
export function useDNSTemplates(enabled = true) {
  return useQuery<DNSTemplateSummary[]>({
    queryKey: ["dns-templates"],
    enabled,
    queryFn: async () => {
      const { data } = await apiClient.get<{ templates: DNSTemplateSummary[] }>("/dns-templates");
      return data?.templates ?? [];
    },
  });
}

// splitTemplateSelection maps the reused mail_provider select value to the
// create-request fields: a "tmpl:<id>" value becomes dns_template_id (with
// mail_provider left undefined so it is omitted from the request), any other
// value stays a plain mail_provider.
export function splitTemplateSelection(sel?: string): {
  mail_provider?: string;
  dns_template_id?: string;
} {
  if (sel && sel.startsWith(DNS_TEMPLATE_VALUE_PREFIX)) {
    return { dns_template_id: sel.slice(DNS_TEMPLATE_VALUE_PREFIX.length) };
  }
  return { mail_provider: sel };
}

// templateOptionGroup returns the antd option group for the admin-defined
// templates, or [] when there are none — so the select is unchanged on a server
// with no templates. Pass disabled to grey the group out (e.g. when the tenant
// has unchecked "Add DNS Zone": a template requires panel-hosted DNS to seed
// into, template_requires_dns).
export function templateOptionGroup(
  templates: DNSTemplateSummary[] | undefined,
  opts?: { disabled?: boolean },
) {
  if (!templates || templates.length === 0) return [];
  return [
    {
      label: "Custom templates",
      options: templates.map((t) => ({
        value: DNS_TEMPLATE_VALUE_PREFIX + t.id,
        label: t.name,
        disabled: opts?.disabled,
      })),
    },
  ];
}
