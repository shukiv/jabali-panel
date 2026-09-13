// DNSTemplatesCard — GH #1627. Admin CRUD for custom DNS templates: a named
// preset of DNS records (A/AAAA/CNAME/MX/TXT/NS/SRV/CAA) an admin defines once
// and a tenant selects at Web Domain / DNS Zone create (the reconciler seeds the
// records into the fresh zone). Mirrors the WebTemplatesCard list+modal shape,
// but the single directives textarea is replaced by a per-record editor because
// a DNS template is a record set, not a blob. Backed by the real REST entity
// (POST/PUT/DELETE /admin/dns-templates); each record is validated server-side
// with the SAME ValidateDNSRecord the tenant DNS record API runs, so the API's
// `detail` ("record 3: ...") is surfaced verbatim in the error toast.
import { useState } from "react";
import {
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  List,
  Modal,
  Popconfirm,
  Select,
  Space,
  Tag,
  Typography,
} from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { DeleteOutlined, EditOutlined, PlusOutlined } from "@icons";
import { apiClient } from "../../../apiClient";

type DNSTemplateRecord = {
  id?: string;
  name: string;
  type: string;
  content: string;
  ttl: number;
  priority: number;
};

type DNSTemplate = {
  id: string;
  name: string;
  description: string;
  records: DNSTemplateRecord[];
  updated_at?: string;
};

type DNSRecordForm = {
  name?: string;
  type: string;
  content: string;
  ttl: number;
  priority?: number;
};

type DNSTemplateForm = {
  name: string;
  description?: string;
  records: DNSRecordForm[];
};

const LIST_KEY = ["admin", "dns-templates"] as const;
// Mirrors the API cap (dnsTemplateMaxRecords) so the form stops adding rows
// before the round-trip rejects the set.
const MAX_RECORDS = 100;
// The exact type set ValidateDNSRecord accepts (dns.go) — a template can never
// carry a record the tenant record API would refuse.
const RECORD_TYPES = ["A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA"];
const DEFAULT_TTL = 3600;

// pickDetail surfaces the API's `detail` (e.g. "record 3: unsupported record
// type" from ValidateDNSRecord) instead of a generic axios message.
const pickDetail = (err: unknown, fallback: string): string => {
  const detail = (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail;
  if (detail) return detail;
  return err instanceof Error ? err.message : fallback;
};

export const DNSTemplatesCard = () => {
  const qc = useQueryClient();
  const [form] = Form.useForm<DNSTemplateForm>();
  const [editing, setEditing] = useState<DNSTemplate | null>(null);
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);

  const list = useQuery<{ templates: DNSTemplate[] }>({
    queryKey: LIST_KEY,
    queryFn: async () => {
      const { data } = await apiClient.get<{ templates: DNSTemplate[] }>("/admin/dns-templates");
      return data;
    },
  });

  const openCreate = () => {
    setEditing(null);
    setOpen(true);
    form.resetFields();
    form.setFieldsValue({ records: [] });
  };

  const openEdit = (row: DNSTemplate) => {
    setEditing(row);
    setOpen(true);
    form.setFieldsValue({
      name: row.name,
      description: row.description,
      records: (row.records ?? []).map((r) => ({
        name: r.name,
        type: r.type,
        content: r.content,
        ttl: r.ttl,
        priority: r.priority,
      })),
    });
  };

  const close = () => {
    setOpen(false);
    setEditing(null);
    form.resetFields();
  };

  const save = async () => {
    let values: DNSTemplateForm;
    try {
      values = await form.validateFields();
    } catch {
      return; // antd renders the field errors
    }
    // Normalise optional numeric/text fields so the payload matches the API's
    // dnsTemplateInput (ttl/priority are plain ints; name defaults to apex "").
    const payload = {
      name: values.name,
      description: values.description ?? "",
      records: (values.records ?? []).map((r) => ({
        name: (r.name ?? "").trim(),
        type: r.type,
        content: r.content,
        ttl: r.ttl ?? DEFAULT_TTL,
        priority: r.priority ?? 0,
      })),
    };
    setSaving(true);
    try {
      if (editing) {
        await apiClient.put(`/admin/dns-templates/${editing.id}`, payload);
        feedback.message.success(`${values.name} saved`);
      } else {
        await apiClient.post("/admin/dns-templates", payload);
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

  const remove = async (row: DNSTemplate) => {
    try {
      await apiClient.delete(`/admin/dns-templates/${row.id}`);
      qc.invalidateQueries({ queryKey: LIST_KEY });
      feedback.message.success(`${row.name} deleted`);
    } catch (err) {
      feedback.message.error(pickDetail(err, "Delete failed"));
    }
  };

  return (
    <>
      <Card
        title="DNS templates"
        style={{ marginBottom: 16 }}
        extra={
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            New template
          </Button>
        }
      >
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          Named presets of DNS records. When a tenant adds a Web Domain or a DNS
          Zone they can pick a template, and its records are seeded into the new
          zone. Use <Typography.Text code>{"{domain}"}</Typography.Text> in a
          record's name or content and it is replaced with the zone's own name at
          seed time. Selecting a template puts the domain in an external mail
          posture (like Microsoft 365 / Google), so the template — not Jabali —
          owns the apex mail records.
        </Typography.Paragraph>
        <List<DNSTemplate>
          loading={list.isLoading}
          dataSource={list.data?.templates ?? []}
          rowKey="id"
          locale={{ emptyText: "No DNS templates yet" }}
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
                    <Tag color="geekblue">
                      {(row.records?.length ?? 0)} record
                      {(row.records?.length ?? 0) === 1 ? "" : "s"}
                    </Tag>
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
        title={editing ? `Edit — ${editing.name}` : "New DNS template"}
        onCancel={close}
        width={900}
        confirmLoading={saving}
        onOk={save}
        okText="Save"
        destroyOnHidden
      >
        <Form<DNSTemplateForm> form={form} layout="vertical">
          <Form.Item
            label="Name"
            name="name"
            rules={[
              { required: true, message: "A name is required" },
              { max: 120, message: "Name cannot exceed 120 characters" },
            ]}
          >
            <Input placeholder="e.g. Acme SaaS" />
          </Form.Item>
          <Form.Item
            label="Description"
            name="description"
            rules={[{ max: 500, message: "Description cannot exceed 500 characters" }]}
          >
            <Input placeholder="Optional" />
          </Form.Item>

          <Typography.Text strong>Records</Typography.Text>
          <Form.List name="records">
            {(fields, { add, remove: removeRow }) => (
              <div style={{ marginTop: 8 }}>
                {fields.map(({ key, name, ...restField }) => (
                  <Space
                    key={key}
                    align="baseline"
                    wrap
                    style={{ display: "flex", marginBottom: 8 }}
                  >
                    <Form.Item
                      {...restField}
                      name={[name, "type"]}
                      initialValue="A"
                      rules={[{ required: true, message: "Type" }]}
                      style={{ marginBottom: 0 }}
                    >
                      <Select
                        style={{ width: 100 }}
                        options={RECORD_TYPES.map((t) => ({ value: t, label: t }))}
                      />
                    </Form.Item>
                    <Form.Item
                      {...restField}
                      name={[name, "name"]}
                      style={{ marginBottom: 0 }}
                    >
                      <Input placeholder="@ · www · {domain}" style={{ width: 160 }} />
                    </Form.Item>
                    <Form.Item
                      {...restField}
                      name={[name, "content"]}
                      rules={[{ required: true, message: "Value" }]}
                      style={{ marginBottom: 0 }}
                    >
                      <Input placeholder="value (e.g. 1.2.3.4 · {domain})" style={{ width: 240 }} />
                    </Form.Item>
                    <Form.Item
                      {...restField}
                      name={[name, "ttl"]}
                      initialValue={DEFAULT_TTL}
                      rules={[{ required: true, message: "TTL" }]}
                      style={{ marginBottom: 0 }}
                    >
                      <InputNumber min={1} placeholder="TTL" style={{ width: 90 }} />
                    </Form.Item>
                    <Form.Item
                      {...restField}
                      name={[name, "priority"]}
                      initialValue={0}
                      tooltip="Used by MX and SRV records"
                      style={{ marginBottom: 0 }}
                    >
                      <InputNumber min={0} placeholder="Prio" style={{ width: 80 }} />
                    </Form.Item>
                    <Button
                      type="text"
                      danger
                      icon={<DeleteOutlined />}
                      onClick={() => removeRow(name)}
                    />
                  </Space>
                ))}
                <Button
                  type="dashed"
                  onClick={() => add({ type: "A", name: "", content: "", ttl: DEFAULT_TTL, priority: 0 })}
                  icon={<PlusOutlined />}
                  disabled={fields.length >= MAX_RECORDS}
                  block
                >
                  Add record{fields.length >= MAX_RECORDS ? " (max 100)" : ""}
                </Button>
              </div>
            )}
          </Form.List>
        </Form>
      </Modal>
    </>
  );
};
