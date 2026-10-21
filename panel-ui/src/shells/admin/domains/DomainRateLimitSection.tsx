// Per-domain request and connection limits (admin only). Both are counted
// per visitor IP address: past the rate, a visitor may burst to twice it,
// then gets 503; past the connection limit, new connections get 503 as
// well. 0 turns a limit off. Applied on the next reconcile.
import { useEffect, useState } from "react";
import { Button, Form, InputNumber, Space, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

// The API refuses larger values (maxDomainRateLimit).
const MAX_LIMIT = 10000;

type Props = {
  domainId: string;
  rateLimitRPS: number;
  connectionLimit: number;
};

type Values = { rate_limit_rps: number | null; connection_limit: number | null };

export const DomainRateLimitSection = ({ domainId, rateLimitRPS, connectionLimit }: Props) => {
  const qc = useQueryClient();
  const [form] = Form.useForm<Values>();
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    form.setFieldsValue({ rate_limit_rps: rateLimitRPS, connection_limit: connectionLimit });
  }, [form, rateLimitRPS, connectionLimit]);

  const onSave = async () => {
    const values = await form.validateFields();
    setSaving(true);
    try {
      await apiClient.patch(`/domains/${domainId}`, {
        rate_limit_rps: values.rate_limit_rps ?? 0,
        connection_limit: values.connection_limit ?? 0,
      });
      feedback.message.success("Limits saved — applied within a minute");
      qc.invalidateQueries({ queryKey: ["one", "domains", domainId] });
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        `Failed to save limits: ${e.response?.data?.detail ?? e.response?.data?.error ?? (err as Error).message}`,
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <Form<Values> form={form} layout="vertical">
      <Typography.Paragraph type="secondary">
        Counted per visitor IP address. 0 turns a limit off.
      </Typography.Paragraph>
      <Space wrap size="large" align="start">
        <Form.Item
          name="rate_limit_rps"
          label="Requests per second"
          extra="A visitor may burst to twice this, then gets 503."
        >
          <InputNumber min={0} max={MAX_LIMIT} precision={0} style={{ width: 200 }} placeholder="0" />
        </Form.Item>
        <Form.Item
          name="connection_limit"
          label="Open connections"
          extra="Connections past this get 503."
        >
          <InputNumber min={0} max={MAX_LIMIT} precision={0} style={{ width: 200 }} placeholder="0" />
        </Form.Item>
      </Space>
      <Button type="primary" loading={saving} onClick={onSave}>
        Save limits
      </Button>
    </Form>
  );
};
