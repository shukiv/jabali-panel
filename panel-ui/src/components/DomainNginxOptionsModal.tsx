import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Checkbox, Form, InputNumber, Modal, Select, Typography, message } from "antd";

import { apiClient } from "../apiClient";

interface SafeOptions {
  max_body_mb?: number;
  hsts?: boolean;
  security_headers?: boolean;
  gzip?: boolean;
  // GH #879 tri-state: absent/null = inherit the server-wide default
  // (Server Settings → Page Templates), true/false = force per-domain.
  intercept_errors?: boolean | null;
}

// The form's Select uses string values; map to the wire tri-state.
type InterceptChoice = "inherit" | "on" | "off";
const interceptToChoice = (v: boolean | null | undefined): InterceptChoice =>
  v === true ? "on" : v === false ? "off" : "inherit";
const choiceToIntercept = (c: InterceptChoice | undefined): boolean | null =>
  c === "on" ? true : c === "off" ? false : null;

export interface DomainNginxOptionsModalProps {
  domainId: string;
  onClose: () => void;
}

// DomainNginxOptionsModal — tenant-facing curated nginx options (GH #307).
// Only mounted when the admin has enabled tenant_domain_options_enabled. The
// panel renders these to fixed, vetted directives; no raw config.
export const DomainNginxOptionsModal = ({ domainId, onClose }: DomainNginxOptionsModalProps) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<SafeOptions & { intercept_choice?: InterceptChoice }>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ nginx_safe_options?: SafeOptions }>(`/domains/${domainId}`);
        if (!cancelled) {
          const opts = resp.data.nginx_safe_options ?? {};
          form.setFieldsValue({
            ...opts,
            intercept_choice: interceptToChoice(opts.intercept_errors),
          });
        }
      } catch {
        if (!cancelled) message.error("Failed to load domain options");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [domainId, form]);

  const onOk = async () => {
    const values = await form.validateFields();
    setSaving(true);
    try {
      await apiClient.patch(`/domains/${domainId}`, {
        nginx_safe_options: {
          max_body_mb: values.max_body_mb ?? 0,
          hsts: !!values.hsts,
          security_headers: !!values.security_headers,
          gzip: !!values.gzip,
          intercept_errors: choiceToIntercept(values.intercept_choice),
        },
      });
      message.success("Domain options saved — applied on the next reconcile");
      onClose();
    } catch {
      message.error("Failed to save domain options");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal open title={t("domainnginxoptionsmodal.domain_options")} onCancel={onClose} onOk={onOk} confirmLoading={saving} okText={t("domainnginxoptionsmodal.save")}>
      <Typography.Paragraph type="secondary">
        Safe nginx options for this domain. The panel renders fixed directives —
        no raw config.
      </Typography.Paragraph>
      <Form<SafeOptions> form={form} layout="vertical" disabled={loading}>
        <Form.Item
          name="max_body_mb"
          label={t("domainnginxoptionsmodal.max_upload_size_mb")}
          tooltip="client_max_body_size. 0 = use the default."
        >
          <InputNumber min={0} max={10240} style={{ width: "100%" }} placeholder="0" />
        </Form.Item>
        <Form.Item name="hsts" valuePropName="checked">
          <Checkbox>Enable HSTS (Strict-Transport-Security)</Checkbox>
        </Form.Item>
        <Form.Item name="security_headers" valuePropName="checked">
          <Checkbox>Add security headers (X-Frame-Options, X-Content-Type-Options, Referrer-Policy)</Checkbox>
        </Form.Item>
        <Form.Item name="gzip" valuePropName="checked">
          <Checkbox>Enable gzip compression for text content</Checkbox>
        </Form.Item>
        <Form.Item
          name="intercept_choice"
          label="Branded error page when the application fails (500-class errors)"
          tooltip="A crashed PHP script normally returns an empty 500, so visitors see the browser's raw error screen. When on, nginx serves the branded 500 page instead (editable under Server Settings → Page Templates); the site keeps its own 404/403 pages. Apps that render their own error screens (e.g. WordPress's recovery notice) or return JSON errors will show the branded page instead — turn it off for those. 'Server default' follows the switch under Server Settings → Page Templates."
          initialValue="inherit"
        >
          <Select
            options={[
              { value: "inherit", label: "Server default" },
              { value: "on", label: "On" },
              { value: "off", label: "Off" },
            ]}
          />
        </Form.Item>
      </Form>
    </Modal>
  );
};
