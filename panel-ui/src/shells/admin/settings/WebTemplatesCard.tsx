// WebTemplatesCard — GH #1624 / ADR-0169 Phase 3b. Admin CRUD for web (nginx)
// templates: a named preset of raw nginx directives an admin can apply to a new
// domain at create (admin-select-only — see DomainCreate's picker). Mirrors the
// PageTemplatesCard list+modal shape, but backed by a real REST entity
// (POST/PUT/DELETE /admin/web-templates) rather than the settings-patch that
// page templates use.
import { useState } from "react";
import { Button, Card, Form, Input, List, Modal, Popconfirm, Space, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { DeleteOutlined, EditOutlined, PlusOutlined } from "@icons";
import { apiClient } from "../../../apiClient";

type WebTemplate = {
  id: string;
  name: string;
  description: string;
  nginx_directives: string;
  updated_at?: string;
};

type WebTemplateForm = {
  name: string;
  description?: string;
  nginx_directives: string;
};

const LIST_KEY = ["admin", "web-templates"] as const;
// Mirrors the API cap (webTemplateMaxDirectivesBytes) so the form rejects an
// over-long blob before the round-trip.
const MAX_BYTES = 16 * 1024;

// pickDetail surfaces the API's `detail` (e.g. the exact rejected directive from
// ValidateNginxDirectivesAdmin) instead of a generic axios message.
const pickDetail = (err: unknown, fallback: string): string => {
  const detail = (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail;
  if (detail) return detail;
  return err instanceof Error ? err.message : fallback;
};

export const WebTemplatesCard = () => {
  const qc = useQueryClient();
  const [form] = Form.useForm<WebTemplateForm>();
  const [editing, setEditing] = useState<WebTemplate | null>(null);
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);

  const list = useQuery<{ templates: WebTemplate[] }>({
    queryKey: LIST_KEY,
    queryFn: async () => {
      const { data } = await apiClient.get<{ templates: WebTemplate[] }>("/admin/web-templates");
      return data;
    },
  });

  const openCreate = () => {
    setEditing(null);
    setOpen(true);
    form.resetFields();
  };

  const openEdit = (row: WebTemplate) => {
    setEditing(row);
    setOpen(true);
    form.setFieldsValue({
      name: row.name,
      description: row.description,
      nginx_directives: row.nginx_directives,
    });
  };

  const close = () => {
    setOpen(false);
    setEditing(null);
    form.resetFields();
  };

  const save = async () => {
    let values: WebTemplateForm;
    try {
      values = await form.validateFields();
    } catch {
      return; // antd renders the field errors
    }
    setSaving(true);
    try {
      if (editing) {
        await apiClient.put(`/admin/web-templates/${editing.id}`, values);
        feedback.message.success(`${values.name} saved`);
      } else {
        await apiClient.post("/admin/web-templates", values);
        feedback.message.success(`${values.name} created`);
      }
      qc.invalidateQueries({ queryKey: LIST_KEY });
      close();
    } catch (err) {
      feedback.message.error(pickDetail(err, "Save failed"));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (row: WebTemplate) => {
    try {
      await apiClient.delete(`/admin/web-templates/${row.id}`);
      qc.invalidateQueries({ queryKey: LIST_KEY });
      feedback.message.success(`${row.name} deleted`);
    } catch (err) {
      feedback.message.error(pickDetail(err, "Delete failed"));
    }
  };

  return (
    <>
      <Card
        title="Web templates (nginx)"
        style={{ marginBottom: 16 }}
        extra={
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            New template
          </Button>
        }
      >
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          Named presets of raw nginx directives. When you create a Web Domain you
          can apply a template to seed its custom directives — the "copy my working
          config" shortcut for common apps (WordPress, Nextcloud, …). Templates are
          admin-authored and validated with the same rules as a domain's own custom
          directives, and can only be applied by an admin at domain create.
        </Typography.Paragraph>
        <List<WebTemplate>
          loading={list.isLoading}
          dataSource={list.data?.templates ?? []}
          rowKey="id"
          locale={{ emptyText: "No web templates yet" }}
          renderItem={(row) => (
            <List.Item
              actions={[
                <Button key="edit" icon={<EditOutlined />} onClick={() => openEdit(row)}>
                  Edit
                </Button>,
                <Popconfirm
                  key="del"
                  title={`Delete ${row.name}?`}
                  okText="Delete"
                  okButtonProps={{ danger: true }}
                  onConfirm={() => remove(row)}
                >
                  <Button danger icon={<DeleteOutlined />}>
                    Delete
                  </Button>
                </Popconfirm>,
              ]}
            >
              <List.Item.Meta
                title={
                  <Space>
                    <Typography.Text strong>{row.name}</Typography.Text>
                    <Tag color="blue">nginx</Tag>
                  </Space>
                }
                description={
                  row.description || (
                    <Typography.Text type="secondary">No description</Typography.Text>
                  )
                }
              />
            </List.Item>
          )}
        />
      </Card>

      <Modal
        open={open}
        title={editing ? `Edit — ${editing.name}` : "New web template"}
        onCancel={close}
        width={900}
        confirmLoading={saving}
        onOk={save}
        okText="Save"
        destroyOnClose
      >
        <Form<WebTemplateForm> form={form} layout="vertical">
          <Form.Item
            label="Name"
            name="name"
            rules={[
              { required: true, message: "A name is required" },
              { max: 120, message: "Name cannot exceed 120 characters" },
            ]}
          >
            <Input placeholder="e.g. WordPress" />
          </Form.Item>
          <Form.Item
            label="Description"
            name="description"
            rules={[{ max: 500, message: "Description cannot exceed 500 characters" }]}
          >
            <Input placeholder="Optional" />
          </Form.Item>
          <Form.Item
            label="Nginx directives"
            name="nginx_directives"
            rules={[
              { required: true, message: "At least one nginx directive is required" },
              {
                validator: (_, v: string) =>
                  new TextEncoder().encode(v ?? "").length > MAX_BYTES
                    ? Promise.reject(new Error(`Directives exceed ${Math.round(MAX_BYTES / 1024)} KB`))
                    : Promise.resolve(),
              },
            ]}
            extra="Raw nginx config injected into the server block. Validated on save: rewrites, headers, proxy_pass and the like are allowed; dangerous directives (root, alias, include, access_log off, auth_basic off) are rejected."
          >
            <Input.TextArea
              spellCheck={false}
              autoSize={{ minRows: 10 }}
              styles={{
                textarea: {
                  fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
                  fontSize: 13,
                  lineHeight: 1.5,
                },
              }}
              placeholder={"location /api/ {\n    proxy_pass http://127.0.0.1:9000;\n}"}
            />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};
