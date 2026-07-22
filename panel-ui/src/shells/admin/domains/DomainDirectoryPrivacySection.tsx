// DomainDirectoryPrivacySection — M50 cPanel-style "Directory Privacy".
// Each rule locks a docroot subdirectory behind HTTP Basic Auth with a
// custom realm; the expanded row holds the per-rule credentials (bcrypt
// hashed at the API, never read back). Rule-creation form collects the
// first credential too so newly-added rules ship with a working login.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
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
import { DeleteOutlined, FolderOpenOutlined, PlusOutlined } from "@icons";

import { apiClient } from "../../../apiClient";
import { DomainPathBrowserModal } from "./DomainPathBrowserModal";
import {
  useCreateDirectoryPrivacyCredential,
  useCreateDirectoryPrivacyRule,
  useDeleteDirectoryPrivacyCredential,
  useDeleteDirectoryPrivacyRule,
  useDirectoryPrivacyCredentials,
  useDirectoryPrivacyRules,
  type CreateCredentialInput,
  type DirectoryPrivacyCredential,
  type DirectoryPrivacyRule,
} from "../../../hooks/useDomainDirectoryPrivacy";

type Props = { domainId: string; domainName?: string };

type NewRuleForm = {
  path: string;
  realm: string;
  username: string;
  password: string;
};

export const DomainDirectoryPrivacySection = ({
  domainId,
  domainName,
}: Props) => {
  const { t } = useTranslation();
  const { data, isLoading } = useDirectoryPrivacyRules(domainId);
  const createRule = useCreateDirectoryPrivacyRule(domainId);
  const deleteRule = useDeleteDirectoryPrivacyRule(domainId);
  // Created rule ID drives the credential POST; mutator hook is keyed by
  // ruleId at construction so we use apiClient directly + invalidate the
  // credential list cache for the new rule after the POST.
  const qc = useQueryClient();
  const [form] = Form.useForm<NewRuleForm>();
  const [adding, setAdding] = useState(false);
  const [browserOpen, setBrowserOpen] = useState(false);
  const realmPlaceholder = domainName || "Restricted";

  if (isLoading && !data) {
    return <Skeleton active paragraph={{ rows: 3 }} />;
  }

  const rows = data?.data ?? [];

  const onAddRule = async (values: NewRuleForm) => {
    try {
      const rule = await createRule.mutateAsync({
        path: values.path.trim(),
        realm: values.realm?.trim() || realmPlaceholder,
      });
      // Second hop: post the first credential under the new rule. The
      // credential mutator hook is keyed by ruleId at construction, so we
      // call apiClient directly and invalidate the per-rule list cache.
      await apiClient.post(
        `/domains/${domainId}/directory-privacy/${rule.id}/credentials`,
        { username: values.username, password: values.password },
      );
      await qc.invalidateQueries({
        queryKey: [
          "list",
          "domain-directory-privacy-credentials",
          domainId,
          rule.id,
        ],
      });
      message.success("Protected directory created");
      form.resetFields();
      setAdding(false);
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string } } })
        ?.response?.data;
      const fallback = err instanceof Error ? err.message : "Failed to add rule";
      message.error(resp?.error ?? fallback);
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
        message={t("domaindirectoryprivacysection.per_directory_password_protection")}
        description={
          <Typography.Paragraph style={{ marginBottom: 0 }}>
            Lock a subdirectory of this domain behind HTTP Basic Auth.{" "}
            <b>Authentication name</b> is the label the browser shows in the
            login popup. Expand a row to add more users; a rule with zero
            credentials denies all access (safer than silently allowing).
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
          title={t("domaindirectoryprivacysection.path")}
          dataIndex="path"
          render={(p: string) => <Typography.Text code>{p}</Typography.Text>}
        />
        <Table.Column<DirectoryPrivacyRule>
          title={t("domaindirectoryprivacysection.authentication_name")}
          dataIndex="realm"
        />
        <Table.Column<DirectoryPrivacyRule>
          title=""
          width={80}
          render={(_, r) => (
            <Popconfirm
              title={t("domaindirectoryprivacysection.delete_this_rule_and_all_its_credentials")}
              onConfirm={() => onDeleteRule(r.id)}
              okText={t("domaindirectoryprivacysection.delete")}
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
        <Form<NewRuleForm>
          form={form}
          layout="vertical"
          onFinish={onAddRule}
          style={{ maxWidth: 520 }}
        >
          <Form.Item
            label={t("domaindirectoryprivacysection.path")}
            name="path"
            tooltip={t("domaindirectoryprivacysection.directory_under_the_docroot_to_protect_e_g_s")}
            rules={[
              { required: true, message: "Required" },
              {
                pattern: /^\/[A-Za-z0-9_./-]+$|^\/$/,
                message: "Start with /, no spaces or special chars",
              },
            ]}
          >
            <Input
              placeholder="/secret"
              addonAfter={
                <a onClick={() => setBrowserOpen(true)}>
                  <FolderOpenOutlined /> Browse
                </a>
              }
            />
          </Form.Item>
          <Form.Item
            label={t("domaindirectoryprivacysection.authentication_name")}
            name="realm"
            tooltip={t("domaindirectoryprivacysection.label_shown_in_the_browser_s_basic_auth_popu")}
          >
            <Input placeholder={realmPlaceholder} />
          </Form.Item>
          <Form.Item
            label={t("domaindirectoryprivacysection.username")}
            name="username"
            rules={[
              { required: true, message: "Required" },
              {
                pattern: /^[A-Za-z0-9._-]{1,64}$/,
                message: "1-64 chars; letters, digits, . _ -",
              },
            ]}
          >
            <Input placeholder="user" />
          </Form.Item>
          <Form.Item
            label={t("domaindirectoryprivacysection.password")}
            name="password"
            rules={[
              { required: true, message: "Required" },
              { min: 8, message: "Min 8 characters" },
              { max: 128, message: "Max 128 characters" },
            ]}
          >
            <Input.Password />
          </Form.Item>
          <Form.Item>
            <Space>
              <Button
                type="primary"
                htmlType="submit"
                loading={createRule.isPending}
              >
                Create
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
      <DomainPathBrowserModal
        domainId={domainId}
        open={browserOpen}
        initialPath={form.getFieldValue("path") || "/"}
        onCancel={() => setBrowserOpen(false)}
        onSelect={(picked) => {
          form.setFieldsValue({ path: picked });
          setBrowserOpen(false);
        }}
      />
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
  const { t } = useTranslation();
  const { data, isLoading } = useDirectoryPrivacyCredentials(domainId, ruleId);
  const create = useCreateDirectoryPrivacyCredential(domainId, ruleId);
  const remove = useDeleteDirectoryPrivacyCredential(domainId, ruleId);
  const [form] = Form.useForm<CreateCredentialInput>();
  const [adding, setAdding] = useState(false);

  const rows = data?.data ?? [];

  const onAdd = async (values: CreateCredentialInput) => {
    try {
      await create.mutateAsync(values);
      message.success("User added");
      form.resetFields();
      setAdding(false);
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string } } })
        ?.response?.data;
      message.error(resp?.error ?? "Failed to add user");
    }
  };

  const onDelete = async (credId: string) => {
    try {
      await remove.mutateAsync({ credId });
      message.success("User deleted");
    } catch {
      message.error("Failed to delete user");
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
          emptyText: "No users — directory is locked (deny by default).",
        }}
      >
        <Table.Column<DirectoryPrivacyCredential>
          title={t("domaindirectoryprivacysection.username")}
          dataIndex="username"
        />
        <Table.Column<DirectoryPrivacyCredential>
          title=""
          width={80}
          render={(_, c) => (
            <Popconfirm
              title={t("domaindirectoryprivacysection.delete_this_user")}
              onConfirm={() => onDelete(c.id)}
              okText={t("domaindirectoryprivacysection.delete")}
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
            label={t("domaindirectoryprivacysection.user")}
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
            label={t("domaindirectoryprivacysection.password")}
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
