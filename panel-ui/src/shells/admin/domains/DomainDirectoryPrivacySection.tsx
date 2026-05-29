// DomainDirectoryPrivacySection — M50 cPanel-style "Directory Privacy".
// Each rule locks a docroot subdirectory behind HTTP Basic Auth with a
// custom realm; the expanded row holds the per-rule credentials (bcrypt
// hashed at the API, never read back).
import { useState } from "react";
import {
  Alert,
  Button,
  Form,
  Input,
  Popconfirm,
  Skeleton,
  Space,
  Table,
  Typography,
  message,
} from "antd";
import { DeleteOutlined, PlusOutlined } from "@icons";

import {
  useCreateDirectoryPrivacyCredential,
  useCreateDirectoryPrivacyRule,
  useDeleteDirectoryPrivacyCredential,
  useDeleteDirectoryPrivacyRule,
  useDirectoryPrivacyCredentials,
  useDirectoryPrivacyRules,
  type CreateCredentialInput,
  type CreateRuleInput,
  type DirectoryPrivacyCredential,
  type DirectoryPrivacyRule,
} from "../../../hooks/useDomainDirectoryPrivacy";

type Props = { domainId: string };

export const DomainDirectoryPrivacySection = ({ domainId }: Props) => {
  const { data, isLoading } = useDirectoryPrivacyRules(domainId);
  const createRule = useCreateDirectoryPrivacyRule(domainId);
  const deleteRule = useDeleteDirectoryPrivacyRule(domainId);
  const [form] = Form.useForm<CreateRuleInput>();
  const [adding, setAdding] = useState(false);

  if (isLoading && !data) {
    return <Skeleton active paragraph={{ rows: 3 }} />;
  }

  const rows = data?.data ?? [];

  const onAddRule = async (values: CreateRuleInput) => {
    try {
      await createRule.mutateAsync({
        path: values.path.trim(),
        realm: values.realm?.trim() || undefined,
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

  const onDeleteRule = async (ruleId: string) => {
    try {
      await deleteRule.mutateAsync({ ruleId });
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
        message="Per-directory password protection"
        description={
          <Typography.Paragraph style={{ marginBottom: 0 }}>
            Lock a subdirectory of this domain behind HTTP Basic Auth.
            Click a row to manage usernames + passwords for that rule. A
            rule with zero credentials denies all access (safer than
            silently allowing).
          </Typography.Paragraph>
        }
      />

      <Table<DirectoryPrivacyRule>
        dataSource={rows}
        rowKey="id"
        pagination={false}
        size="small"
        scroll={{ x: "max-content" }}
        locale={{ emptyText: "No protected directories." }}
        expandable={{
          expandedRowRender: (rule) => (
            <DirectoryPrivacyCredentialsTable
              domainId={domainId}
              ruleId={rule.id}
            />
          ),
        }}
      >
        <Table.Column<DirectoryPrivacyRule>
          title="Path"
          dataIndex="path"
          render={(p: string) => <Typography.Text code>{p}</Typography.Text>}
        />
        <Table.Column<DirectoryPrivacyRule>
          title="Realm"
          dataIndex="realm"
        />
        <Table.Column<DirectoryPrivacyRule>
          title=""
          width={80}
          render={(_, r) => (
            <Popconfirm
              title="Delete this rule and all its credentials?"
              onConfirm={() => onDeleteRule(r.id)}
              okText="Delete"
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
        <Form<CreateRuleInput>
          form={form}
          layout="inline"
          onFinish={onAddRule}
          initialValues={{ realm: "Restricted" }}
        >
          <Form.Item
            label="Path"
            name="path"
            rules={[
              { required: true, message: "Required" },
              {
                pattern: /^\/[A-Za-z0-9_./-]+$/,
                message: "Start with /, no spaces or special chars",
              },
            ]}
          >
            <Input placeholder="/secret" style={{ width: 220 }} />
          </Form.Item>
          <Form.Item label="Realm" name="realm">
            <Input placeholder="Restricted" style={{ width: 180 }} />
          </Form.Item>
          <Form.Item>
            <Space>
              <Button
                type="primary"
                htmlType="submit"
                loading={createRule.isPending}
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
          Add protected directory
        </Button>
      )}
    </Space>
  );
};

const DirectoryPrivacyCredentialsTable = ({
  domainId,
  ruleId,
}: {
  domainId: string;
  ruleId: string;
}) => {
  const { data, isLoading } = useDirectoryPrivacyCredentials(domainId, ruleId);
  const create = useCreateDirectoryPrivacyCredential(domainId, ruleId);
  const remove = useDeleteDirectoryPrivacyCredential(domainId, ruleId);
  const [form] = Form.useForm<CreateCredentialInput>();
  const [adding, setAdding] = useState(false);

  const rows = data?.data ?? [];

  const onAdd = async (values: CreateCredentialInput) => {
    try {
      await create.mutateAsync(values);
      message.success("Credential added");
      form.resetFields();
      setAdding(false);
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string } } })
        ?.response?.data;
      message.error(resp?.error ?? "Failed to add credential");
    }
  };

  const onDelete = async (credId: string) => {
    try {
      await remove.mutateAsync({ credId });
      message.success("Credential deleted");
    } catch {
      message.error("Failed to delete credential");
    }
  };

  return (
    <Space direction="vertical" style={{ width: "100%" }} size="small">
      <Table<DirectoryPrivacyCredential>
        dataSource={rows}
        rowKey="id"
        loading={isLoading}
        pagination={false}
        size="small"
        locale={{
          emptyText: "No credentials — directory is locked (deny by default).",
        }}
      >
        <Table.Column<DirectoryPrivacyCredential>
          title="Username"
          dataIndex="username"
        />
        <Table.Column<DirectoryPrivacyCredential>
          title=""
          width={80}
          render={(_, c) => (
            <Popconfirm
              title="Delete this credential?"
              onConfirm={() => onDelete(c.id)}
              okText="Delete"
              okButtonProps={{ danger: true }}
            >
              <Button
                size="small"
                danger
                icon={<DeleteOutlined />}
              />
            </Popconfirm>
          )}
        />
      </Table>

      {adding ? (
        <Form<CreateCredentialInput>
          form={form}
          layout="inline"
          onFinish={onAdd}
        >
          <Form.Item
            label="User"
            name="username"
            rules={[
              { required: true, message: "Required" },
              {
                pattern: /^[A-Za-z0-9._-]{1,64}$/,
                message: "1-64 chars; letters, digits, . _ -",
              },
            ]}
          >
            <Input style={{ width: 160 }} />
          </Form.Item>
          <Form.Item
            label="Password"
            name="password"
            rules={[
              { required: true, message: "Required" },
              { min: 8, message: "Min 8 characters" },
              { max: 128, message: "Max 128 characters" },
            ]}
          >
            <Input.Password style={{ width: 200 }} />
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
        <Button
          size="small"
          icon={<PlusOutlined />}
          onClick={() => setAdding(true)}
        >
          Add user
        </Button>
      )}
    </Space>
  );
};
