// DomainAdvancedDirectivesPanel — GH #1624 / ADR-0169 Phase 4b. Tenant-facing
// raw "advanced directives" for a single domain, gated (like the curated safe
// options and the rewrite rule builder) on tenant_domain_options_enabled.
//
// Unlike the admin-only nginx_custom_directives, the tenant field is confined by
// a tight value-grammar on the backend (ValidateNginxDirectivesTenant): one
// statement per line, no blocks, and only add_header / expires / etag — so it
// cannot SSRF, disclose files, or suppress logging. The panel surfaces the
// backend's exact rejection reason (the 400 `detail`) so a tenant sees which
// line was refused rather than a generic error.
import { useEffect, useState } from "react";
import { Button, Form, Input, Typography } from "antd";
import { feedback } from "../lib/feedback";
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

// Mirrors the API cap (tenantDirectivesMaxBytes) so the form rejects an
// over-long blob before the round-trip.
const MAX_BYTES = 8 * 1024;

// pickDetail surfaces the backend's `detail` (the exact rejected directive from
// ValidateNginxDirectivesTenant) instead of a generic axios message.
const pickDetail = (err: unknown, fallback: string): string => {
  const detail = (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail;
  if (detail) return detail;
  return err instanceof Error ? err.message : fallback;
};

export interface DomainAdvancedDirectivesPanelProps {
  domainId: string;
  onSaved?: () => void;
}

interface Values {
  nginx_tenant_directives?: string;
}

export const DomainAdvancedDirectivesPanel = ({ domainId, onSaved }: DomainAdvancedDirectivesPanelProps) => {
  const qc = useQueryClient();
  const [form] = Form.useForm<Values>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ nginx_tenant_directives?: string }>(`/domains/${domainId}`);
        if (!cancelled) {
          form.setFieldsValue({ nginx_tenant_directives: resp.data.nginx_tenant_directives ?? "" });
        }
      } catch {
        if (!cancelled) feedback.message.error("Failed to load advanced directives");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [domainId, form]);

  const onSave = async () => {
    let values: Values;
    try {
      values = await form.validateFields();
    } catch {
      return; // antd renders the field error
    }
    setSaving(true);
    try {
      await apiClient.patch(`/domains/${domainId}`, {
        nginx_tenant_directives: values.nginx_tenant_directives ?? "",
      });
      feedback.message.success("Advanced directives saved — applied on the next reconcile");
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
      qc.invalidateQueries({ queryKey: ["one", "domains", domainId] });
      onSaved?.();
    } catch (err) {
      feedback.message.error(pickDetail(err, "Failed to save advanced directives"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <Typography.Paragraph type="secondary">
        Extra nginx directives for this domain, one per line. Only response and
        caching directives are allowed — <code>add_header</code>,{" "}
        <code>expires</code> and <code>etag</code>. Each line must be a single
        complete statement ending in <code>;</code>. Blocks,{" "}
        <code>proxy_pass</code>, <code>root</code> and other routing or file
        directives stay admin-only. Panel-managed response headers (HSTS,
        X-Frame-Options, …) can't be overridden here — use the Domain options
        tab for those.
      </Typography.Paragraph>
      <Form<Values> form={form} layout="vertical" disabled={loading}>
        <Form.Item
          name="nginx_tenant_directives"
          rules={[
            {
              validator: (_, v: string) =>
                new TextEncoder().encode(v ?? "").length > MAX_BYTES
                  ? Promise.reject(new Error(`Directives exceed ${Math.round(MAX_BYTES / 1024)} KB`))
                  : Promise.resolve(),
            },
          ]}
        >
          <Input.TextArea
            spellCheck={false}
            autoSize={{ minRows: 6 }}
            style={{ fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace", fontSize: 13 }}
            placeholder={"add_header X-Robots-Tag noindex;\nexpires 30d;\netag on;"}
          />
        </Form.Item>
        <Button type="primary" loading={saving} disabled={loading} onClick={onSave}>
          Save
        </Button>
      </Form>
    </div>
  );
};
