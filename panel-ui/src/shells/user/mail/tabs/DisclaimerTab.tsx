// DisclaimerTab — M6.5 Step 6. Per-domain outbound disclaimer.
// Mockup: row click → modal with Enable toggle + Disclaimer Text TextArea + Save/Cancel.

import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import {
  Button,
  
  Empty,
  Form,
  Input,
  message,
  Modal,
  Skeleton,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { EditOutlined, FileTextOutlined } from "@icons";
import { useQueries } from "@tanstack/react-query";

import { apiClient } from "../../../../apiClient";
import { useListQuery } from "../../../../hooks/useQueries";
import { useUpdateDisclaimer, type Disclaimer } from "../../../../hooks/useDisclaimer";
import type { Domain } from "../../domains/UserDomainList";

interface FormValues {
  enabled: boolean;
  text: string;
}

export const DisclaimerTab = () => {
  const { t } = useTranslation();
  const { items: domains, isLoading: loadingDomains } = useListQuery<Domain>({
    resource: "domains",
    params: { page: 1, pageSize: 200, sort: "name", order: "asc" },
  });
  const emailEnabled = useMemo(() => domains.filter((d) => d.email_enabled), [domains]);

  const results = useQueries({
    queries: emailEnabled.map((d) => ({
      queryKey: ["disclaimer", d.id],
      queryFn: async () => {
        const { data } = await apiClient.get<Disclaimer>(`/domains/${d.id}/disclaimer`);
        return data;
      },
    })),
  });

  const rows = useMemo(() => results.filter((r) => r.data).map((r) => r.data as Disclaimer), [results]);
  const anyLoading = results.some((r) => r.isLoading);

  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Disclaimer | null>(null);
  const [form] = Form.useForm<FormValues>();
  const updateMut = useUpdateDisclaimer();

  const openEdit = (row: Disclaimer) => {
    setEditing(row);
    form.setFieldsValue({ enabled: row.enabled, text: row.text });
    setOpen(true);
  };

  const submit = async () => {
    const vals = await form.validateFields();
    if (!editing) return;
    try {
      await updateMut.mutateAsync({
        domainID: editing.domain_id,
        enabled: vals.enabled,
        text: vals.text,
      });
      message.success("Disclaimer saved");
      setOpen(false);
    } catch (err) {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error
        ?? "Failed to save disclaimer";
      message.error(msg);
    }
  };

  if (loadingDomains && domains.length === 0) return <Skeleton active paragraph={{ rows: 4 }} />;
  if (emailEnabled.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("disclaimertab.no_email_enabled_domains")} />;
  }

  return (
    <>
      <div>
        <Typography.Title level={3} style={{ marginTop: 0 }}>
          Disclaimer
        </Typography.Title>
        <Typography.Paragraph type="secondary">
          Append disclaimer text to outbound mail from a domain. Applied at the mail server to both
          plain-text and HTML body parts.
        </Typography.Paragraph>

        <Table
          rowKey="domain_id"
          loading={anyLoading && rows.length === 0}
          dataSource={rows}
          pagination={false}
          scroll={{ x: "max-content" }}
          columns={[
            {
              title: "Domain",
              dataIndex: "domain_name",
              sorter: (a: Disclaimer, b: Disclaimer) => a.domain_name.localeCompare(b.domain_name),
            },
            {
              title: "Status",
              dataIndex: "enabled",
              width: 120,
              render: (v: boolean) =>
                v ? <Tag color="green">enabled</Tag> : <Tag>disabled</Tag>,
            },
            {
              title: "Preview",
              dataIndex: "text",
              render: (v: string) => {
                if (!v) return <Typography.Text type="secondary">— (not set)</Typography.Text>;
                const preview = v.length > 80 ? v.slice(0, 80) + "…" : v;
                return <Typography.Text>{preview}</Typography.Text>;
              },
            },
            {
              title: "Actions",
              width: 100,
              render: (_, row) => (
                <Tooltip title={t("disclaimertab.edit")}>
                  <Button type="text" icon={<EditOutlined />} onClick={() => openEdit(row)} />
                </Tooltip>
              ),
            },
          ]}
        />
      </div>

      <Modal
        open={open}
        title={
          <Space>
            <FileTextOutlined />
            <span>Email Disclaimer</span>
            {editing && <Typography.Text type="secondary" style={{ fontWeight: "normal" }}>{editing.domain_name}</Typography.Text>}
          </Space>
        }
        onCancel={() => setOpen(false)}
        onOk={submit}
        okText={t("disclaimertab.save")}
        confirmLoading={updateMut.isPending}
        destroyOnClose
        width={640}
      >
        <Form form={form} layout="vertical" preserve={false}>
          <Form.Item name="enabled" label={t("disclaimertab.enable_disclaimer")} valuePropName="checked" extra="Append a disclaimer to all outbound emails from this domain">
            <Switch />
          </Form.Item>
          <Form.Item
            name="text"
            label={t("disclaimertab.disclaimer_text")}
            rules={[
              ({ getFieldValue }) => ({
                validator(_, value) {
                  if (getFieldValue("enabled") && !value?.trim()) {
                    return Promise.reject(new Error("Text required when enabled"));
                  }
                  return Promise.resolve();
                },
              }),
            ]}
            extra="This text will be appended to every outgoing email"
          >
            <Input.TextArea rows={5} placeholder={t("disclaimertab.if_you_received_this_email_by_mistake_please")} />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};
