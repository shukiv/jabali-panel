// DomainIPACLSection — M36 per-domain IP allow/deny rules. Renders
// a table of existing rules (priority asc) with delete + an inline
// add form. nginx interprets `allow` and `deny` directives top-down
// inside the server block; lower priority = higher precedence.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Alert,
  Button,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Select,
  Skeleton,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from "antd";
import { DeleteOutlined, PlusOutlined } from "@icons";

import {
  useCreateDomainIPACL,
  useDeleteDomainIPACL,
  useDomainIPACLs,
  type ACLAction,
  type CreateACLInput,
  type DomainIPACL,
} from "../../../hooks/useDomainIPACL";

type Props = { domainId: string };

const ACTION_OPTIONS: { value: ACLAction; label: string }[] = [
  { value: "allow", label: "Allow" },
  { value: "deny", label: "Deny" },
];

const ACTION_TAG: Record<ACLAction, { color: string; label: string }> = {
  allow: { color: "green", label: "ALLOW" },
  deny: { color: "red", label: "DENY" },
};

export const DomainIPACLSection = ({ domainId }: Props) => {
  const { t } = useTranslation();
  const { data, isLoading } = useDomainIPACLs(domainId);
  const create = useCreateDomainIPACL(domainId);
  const remove = useDeleteDomainIPACL(domainId);
  const [form] = Form.useForm<CreateACLInput>();
  const [adding, setAdding] = useState(false);

  if (isLoading && !data) {
    return <Skeleton active paragraph={{ rows: 3 }} />;
  }

  const rows = data?.data ?? [];

  const onAdd = async (values: CreateACLInput) => {
    try {
      await create.mutateAsync({
        cidr: values.cidr.trim(),
        action: values.action,
        priority: values.priority ?? 0,
        comment: values.comment ?? "",
      });
      message.success("Rule added");
      form.resetFields();
      setAdding(false);
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string } } })
        ?.response?.data;
      message.error(resp?.error ?? "Failed to add rule");
    }
  };

  const onDelete = async (aclID: string) => {
    try {
      await remove.mutateAsync({ aclID });
      message.success("Rule deleted");
    } catch {
      message.error("Failed to delete rule");
    }
  };

  return (
    <Space direction="vertical" style={{ width: "100%" }} size="middle">
      <Alert
        type="info"
        showIcon
        message={t("domainipaclsection.per_domain_ip_allow_deny")}
        description={
          <Typography.Paragraph style={{ marginBottom: 0 }}>
            Rules are evaluated by nginx top-down (lower priority = higher
            precedence). First match wins. Use a final <code>0.0.0.0/0</code>{" "}
            <Tag color="red">deny</Tag> to switch from open to allowlist mode
            after adding the trusted ranges.
          </Typography.Paragraph>
        }
      />

      <Table<DomainIPACL>
        dataSource={rows}
        rowKey="id"
        pagination={false}
        size="small"
        scroll={{ x: "max-content" }}
        locale={{ emptyText: "No rules — domain accepts all traffic." }}
      >
        <Table.Column<DomainIPACL>
          title={t("domainipaclsection.priority")}
          dataIndex="priority"
          width={80}
        />
        <Table.Column<DomainIPACL>
          title={t("domainipaclsection.action")}
          dataIndex="action"
          width={100}
          render={(a: ACLAction) => {
            const t = ACTION_TAG[a];
            return <Tag color={t.color}>{t.label}</Tag>;
          }}
        />
        <Table.Column<DomainIPACL>
          title={t("domainipaclsection.cidr")}
          dataIndex="cidr"
          render={(c: string) => <Typography.Text code>{c}</Typography.Text>}
        />
        <Table.Column<DomainIPACL>
          title={t("domainipaclsection.comment")}
          dataIndex="comment"
          render={(c: string) =>
            c ? (
              <Typography.Text>{c}</Typography.Text>
            ) : (
              <Typography.Text type="secondary">—</Typography.Text>
            )
          }
        />
        <Table.Column<DomainIPACL>
          title=""
          width={80}
          render={(_, r) => (
            <Popconfirm
              title={t("domainipaclsection.delete_this_rule")}
              onConfirm={() => onDelete(r.id)}
              okText={t("domainipaclsection.delete")}
              okButtonProps={{ danger: true }}
            >
              <Button
                size="small"
                type="primary"
                danger
                icon={<DeleteOutlined />}
              />
            </Popconfirm>
          )}
        />
      </Table>

      {adding ? (
        <Form<CreateACLInput>
          form={form}
          layout="inline"
          onFinish={onAdd}
          initialValues={{ action: "allow", priority: 100 }}
        >
          <Form.Item
            label={t("domainipaclsection.cidr")}
            name="cidr"
            rules={[{ required: true, message: "Required" }]}
          >
            <Input placeholder="203.0.113.0/24" style={{ width: 180 }} />
          </Form.Item>
          <Form.Item
            label={t("domainipaclsection.action")}
            name="action"
            rules={[{ required: true }]}
          >
            <Select options={ACTION_OPTIONS} style={{ width: 100 }} />
          </Form.Item>
          <Form.Item label={t("domainipaclsection.priority")} name="priority">
            <InputNumber min={0} max={9999} style={{ width: 90 }} />
          </Form.Item>
          <Form.Item label={t("domainipaclsection.comment")} name="comment">
            <Input style={{ width: 200 }} />
          </Form.Item>
          <Form.Item>
            <Space>
              <Button
                type="primary"
                htmlType="submit"
                loading={create.isPending}
              >
                Add
              </Button>
              <Button
                onClick={() => {
                  form.resetFields();
                  setAdding(false);
                }}
              >
                Cancel
              </Button>
            </Space>
          </Form.Item>
        </Form>
      ) : (
        <Button icon={<PlusOutlined />} onClick={() => setAdding(true)}>
          Add rule
        </Button>
      )}
    </Space>
  );
};
