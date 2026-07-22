// MailThrottlesPage — M47 Wave 3 admin outbound-throttle CRUD.
//
// DB-as-truth: rows on this page are mail_outbound_policy rows. The
// reconciler converges each row into Stalwart's MtaOutboundThrottle
// on the next tick (5s typical). last_applied_at + last_error on each
// row tell the operator whether the Stalwart side caught up.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { RowActions } from "../../../components/RowActions";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Drawer, Form, Input, InputNumber, Select, Space, Switch, Table, Tag, Typography, message } from "antd";
import { EditOutlined, DeleteOutlined } from "@icons";
import type { ColumnsType } from "antd/es/table";

import { apiClient } from "../../../apiClient";

type Policy = {
  id: string;
  scope: "user" | "domain" | "global";
  scope_ref: string | null;
  max_per_hour: number;
  max_per_day: number;
  enabled: boolean;
  stalwart_id: string;
  last_applied_at: string | null;
  last_error: string | null;
  created_at: string;
  updated_at: string;
};

export const MailThrottlesPage = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [drawer, setDrawer] = useState<{ open: boolean; row?: Policy }>({ open: false });
  const [form] = Form.useForm();

  const { data, isLoading } = useQuery({
    queryKey: ["admin", "mail", "throttles"],
    queryFn: async () => (await apiClient.get<{ items: Policy[] }>("/admin/mail/throttles")).data.items,
  });

  const createMut = useMutation({
    mutationFn: (body: Partial<Policy>) => apiClient.post("/admin/mail/throttles", body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "mail", "throttles"] });
      message.success("Throttle created — reconciler will push to Stalwart on next tick");
      setDrawer({ open: false });
    },
    onError: (e: any) => message.error(e?.response?.data?.error ?? "create failed"),
  });

  const updateMut = useMutation({
    mutationFn: (b: { id: string; body: Partial<Policy> }) => apiClient.put(`/admin/mail/throttles/${b.id}`, b.body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "mail", "throttles"] });
      message.success("Throttle updated");
      setDrawer({ open: false });
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => apiClient.delete(`/admin/mail/throttles/${id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "mail", "throttles"] });
      message.success("Throttle removed");
    },
  });

  const onSubmit = async () => {
    const v = await form.validateFields();
    const payload = { ...v, scope_ref: v.scope === "global" ? null : v.scope_ref };
    if (drawer.row) {
      updateMut.mutate({ id: drawer.row.id, body: payload });
    } else {
      createMut.mutate(payload);
    }
  };

  const columns: ColumnsType<Policy> = [
    { title: "Scope", dataIndex: "scope", render: (s) => <Tag color={s === "global" ? "blue" : s === "user" ? "geekblue" : "purple"}>{s}</Tag> },
    { title: "Ref", dataIndex: "scope_ref", render: (r) => r ?? <Typography.Text type="secondary">—</Typography.Text> },
    { title: "Per hour", dataIndex: "max_per_hour", render: (n) => (n === 0 ? "∞" : n) },
    { title: "Per day", dataIndex: "max_per_day", render: (n) => (n === 0 ? "∞" : n) },
    { title: "Enabled", dataIndex: "enabled", render: (b) => (b ? <Tag color="green">on</Tag> : <Tag>off</Tag>) },
    {
      title: "Stalwart sync",
      key: "sync",
      render: (_, row) => {
        if (row.last_error) {
          return <Tag color="red" title={row.last_error}>error</Tag>;
        }
        if (!row.stalwart_id) {
          return <Tag>pending</Tag>;
        }
        return <Tag color="green">synced</Tag>;
      },
    },
    {
      title: "Actions",
      key: "act",
      render: (_, row) => (
        <RowActions
          actions={[
            { key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => { form.setFieldsValue(row); setDrawer({ open: true, row }); } },
            {
              key: "delete",
              label: "Delete",
              icon: <DeleteOutlined />,
              danger: true,
              onClick: () => deleteMut.mutate(row.id),
              confirm: { title: "Delete throttle?", description: `${row.scope}/${row.scope_ref ?? "-"} — ${row.max_per_hour}/hr`, okText: "Delete" },
            },
          ]}
        />
      ),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 16, justifyContent: "space-between", width: "100%" }}>
        <Typography.Title level={4} style={{ margin: 0 }}>
          Outbound throttles
        </Typography.Title>
        <Button type="primary" onClick={() => { form.resetFields(); setDrawer({ open: true }); }}>
          New throttle
        </Button>
      </Space>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message={t("mailthrottlespage.per_account_per_domain_server_wide_outbound")}
        description={t("mailthrottlespage.each_row_converges_into_a_stalwart_mtaoutbou")}
      />
      <Table rowKey="id" loading={isLoading} dataSource={data ?? []} columns={columns} pagination={false} />
      <Drawer
        title={drawer.row ? "Edit throttle" : "New throttle"}
        open={drawer.open}
        onClose={() => setDrawer({ open: false })}
        width={420}
        extra={
          <Space>
            <Button onClick={() => setDrawer({ open: false })}>Cancel</Button>
            <Button type="primary" onClick={onSubmit} loading={createMut.isPending || updateMut.isPending}>
              Save
            </Button>
          </Space>
        }
      >
        <Form form={form} layout="vertical" initialValues={{ scope: "global", enabled: true }}>
          <Form.Item name="scope" label={t("mailthrottlespage.scope")} rules={[{ required: true }]}>
            <Select disabled={!!drawer.row}>
              <Select.Option value="global">global (server-wide)</Select.Option>
              <Select.Option value="user">user</Select.Option>
              <Select.Option value="domain">domain</Select.Option>
            </Select>
          </Form.Item>
          <Form.Item shouldUpdate={(p, n) => p.scope !== n.scope} noStyle>
            {({ getFieldValue }) =>
              getFieldValue("scope") !== "global" ? (
                <Form.Item name="scope_ref" label={t("mailthrottlespage.scope_ref_ulid")} rules={[{ required: true }]}>
                  <Input placeholder={getFieldValue("scope") === "user" ? "users.id" : "domains.id"} disabled={!!drawer.row} />
                </Form.Item>
              ) : null
            }
          </Form.Item>
          <Form.Item name="max_per_hour" label={t("mailthrottlespage.max_per_hour_0_unlimited")}>
            <InputNumber min={0} max={1000000} style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item name="max_per_day" label={t("mailthrottlespage.max_per_day_logged_only_v1")}>
            <InputNumber min={0} max={10000000} style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item name="enabled" label={t("mailthrottlespage.enabled")} valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Drawer>
    </div>
  );
};
