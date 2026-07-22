import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Checkbox, Form, InputNumber, Modal, Typography, message } from "antd";

import { apiClient } from "../apiClient";

interface SafeOptions {
  max_body_mb?: number;
  hsts?: boolean;
  security_headers?: boolean;
  gzip?: boolean;
}

export interface DomainNginxOptionsModalProps {
  domainId: string;
  onClose: () => void;
}

// DomainNginxOptionsModal — tenant-facing curated nginx options (GH #307).
// Only mounted when the admin has enabled tenant_domain_options_enabled. The
// panel renders these to fixed, vetted directives; no raw config.
export const DomainNginxOptionsModal = ({ domainId, onClose }: DomainNginxOptionsModalProps) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<SafeOptions>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ nginx_safe_options?: SafeOptions }>(`/domains/${domainId}`);
        if (!cancelled) form.setFieldsValue(resp.data.nginx_safe_options ?? {});
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
      </Form>
    </Modal>
  );
};
