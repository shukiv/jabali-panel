import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Drawer,
  Flex,
  Form,
  Grid,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from "antd";
import { useQuery } from "@tanstack/react-query";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import {
  PlusSquareOutlined,
  DeleteOutlined,
  KeyOutlined,
  CloudServerOutlined,
} from "@icons";
import { RowActionButton } from "../../../components/RowActionButton";
import { PasswordInput } from "../../../components/PasswordInput";
import { StandardDrawerFooter } from "../../../components/StandardActionFooter";
import {
  listFtpAccounts,
  createFtpAccount,
  updateFtpAccount,
  resetFtpAccountPassword,
  deleteFtpAccount,
  type FtpAccount,
} from "../../../apiClient";
import { getIdentity } from "../../../identity";
import { useServerCapabilities } from "../../../hooks/useServerCapabilities";

// UserFtpAccountsPage — tenant self-service FTP/SFTP subaccounts (GH #1053).
// Every account is a same-uid alias of the tenant: its own username +
// password, files always owned by the tenant. SFTP always works; FTPS only
// when the admin enabled the server-level FTP module (ftp_server_enabled).
export const UserFtpAccountsPage = () => {
  const { t } = useTranslation();
  const [form] = Form.useForm();
  const [pwForm] = Form.useForm();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [pwTarget, setPwTarget] = useState<FtpAccount | null>(null);
  const [saving, setSaving] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? true;

  const { data: caps } = useServerCapabilities();
  // GH #1171: isolated accounts need filesystem disk quota. Where it isn't
  // enabled, default the toggle off (and disable it) instead of defaulting on
  // and failing at create; keep the #1145 isolated default where it works.
  const isoAvailable = caps?.ftp_isolation_available ?? false;
  const { data: me } = useQuery({ queryKey: ["identity"], queryFn: getIdentity });
  const username = me?.username ?? "";

  const {
    data: listResponse = { data: [], total: 0 },
    isLoading,
    refetch,
  } = useQuery({
    queryKey: ["ftp-accounts"],
    queryFn: listFtpAccounts,
  });
  const accounts = listResponse.data ?? [];

  const [search, setSearch] = useState("");
  const filtered = useMemo(() => {
    if (!search) return accounts;
    const needle = search.toLowerCase();
    return accounts.filter(
      (a) => a.username.toLowerCase().includes(needle) || a.home_path.toLowerCase().includes(needle),
    );
  }, [accounts, search]);

  const handleCreate = async (values: {
    label: string;
    home_rel?: string;
    password: string;
    ftp_access?: boolean;
    isolated?: boolean;
    quota_mb?: number;
  }) => {
    setSaving(true);
    try {
      // The /home/<user> prefix is a fixed addon — users type only the
      // part inside their home. Empty = the home directory itself.
      const rel = (values.home_rel ?? "").replace(/^\/+|\/+$/g, "");
      const isolated = isoAvailable && values.isolated !== false;
      await createFtpAccount({
        label: values.label,
        home_path: rel ? `${homePrefix}/${rel}` : homePrefix,
        password: values.password,
        ftp_access: !!values.ftp_access,
        isolated,
        quota_mb: isolated ? values.quota_mb : undefined,
      });
      feedback.message.success(t("ftpaccounts.created"));
      setDrawerOpen(false);
      form.resetFields();
      refetch();
    } catch (err: unknown) {
      feedback.message.error(extractApiError(err, t("ftpaccounts.create_failed")));
    } finally {
      setSaving(false);
    }
  };

  const handleToggle = async (row: FtpAccount, patch: Partial<FtpAccount>) => {
    setBusyId(row.id);
    try {
      await updateFtpAccount(row.id, patch);
      refetch();
    } catch (err: unknown) {
      feedback.message.error(extractApiError(err, t("ftpaccounts.update_failed")));
    } finally {
      setBusyId(null);
    }
  };

  const handlePassword = async (values: { password: string }) => {
    if (!pwTarget) return;
    setSaving(true);
    try {
      await resetFtpAccountPassword(pwTarget.id, values.password);
      feedback.message.success(t("ftpaccounts.password_updated"));
      setPwTarget(null);
      pwForm.resetFields();
    } catch (err: unknown) {
      feedback.message.error(extractApiError(err, t("ftpaccounts.password_failed")));
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (row: FtpAccount) => {
    setBusyId(row.id);
    try {
      await deleteFtpAccount(row.id);
      feedback.message.success(t("ftpaccounts.deleted"));
      refetch();
    } catch (err: unknown) {
      feedback.message.error(extractApiError(err, t("ftpaccounts.delete_failed")));
    } finally {
      setBusyId(null);
    }
  };

  const homePrefix = username ? `/home/${username}` : "/home/<you>";

  return (
    <Flex vertical gap={16}>
      <Typography.Title level={3} style={{ margin: 0 }}>
        <CloudServerOutlined /> {t("ftpaccounts.title")}
      </Typography.Title>

      <Alert
        type="info"
        showIcon
        message={t("ftpaccounts.connection_info")}
        description={
          <>
            <div>
              {t("ftpaccounts.sftp_line", {
                host: window.location.hostname,
              })}
            </div>
            {caps?.ftp_server_enabled ? (
              <div>{t("ftpaccounts.ftps_line", { host: window.location.hostname })}</div>
            ) : (
              <div>{t("ftpaccounts.sftp_only_line")}</div>
            )}
            <div>{t("ftpaccounts.files_owned_line")}</div>
          </>
        }
      />

      <Card>
        <Flex justify="space-between" wrap gap={8} style={{ marginBottom: 16 }}>
          <Input.Search
            allowClear
            placeholder={t("ftpaccounts.search_placeholder")}
            style={{ maxWidth: 320 }}
            onChange={(e) => setSearch(e.target.value)}
          />
          <Button type="primary" icon={<PlusSquareOutlined />} onClick={() => setDrawerOpen(true)}>
            {t("ftpaccounts.add_account")}
          </Button>
        </Flex>

        <Table<FtpAccount>
          rowKey="id"
          loading={isLoading}
          dataSource={filtered}
          pagination={false}
          scroll={{ x: "max-content" }}
        >
          <Table.Column<FtpAccount>
            dataIndex="username"
            title={t("ftpaccounts.username")}
            render={(v: string, row) => (
              <Space size={4} wrap>
                <Typography.Text code copyable>{v}</Typography.Text>
                <Tag color={row.isolated ? "green" : "default"}>
                  {row.isolated ? t("ftpaccounts.isolated_tag") : t("ftpaccounts.shared_tag")}
                </Tag>
              </Space>
            )}
          />
          <Table.Column<FtpAccount>
            dataIndex="home_path"
            title={t("ftpaccounts.directory")}
            render={(v: string) => <Typography.Text code>{v}</Typography.Text>}
          />
          <Table.Column<FtpAccount>
            dataIndex="sftp_access"
            title="SFTP"
            render={(v: boolean, row) => (
              <Switch
                size="small"
                checked={v}
                loading={busyId === row.id}
                onChange={(next) => handleToggle(row, { sftp_access: next })}
              />
            )}
          />
          <Table.Column<FtpAccount>
            dataIndex="ftp_access"
            title="FTPS"
            render={(v: boolean, row) => (
              <Switch
                size="small"
                checked={v}
                disabled={!caps?.ftp_server_enabled && !v}
                loading={busyId === row.id}
                onChange={(next) => handleToggle(row, { ftp_access: next })}
              />
            )}
          />
          <Table.Column<FtpAccount>
            dataIndex="is_enabled"
            title={t("ftpaccounts.status")}
            render={(v: boolean) => (
              <Tag color={v ? "green" : "red"}>
                {v ? t("ftpaccounts.active") : t("ftpaccounts.disabled")}
              </Tag>
            )}
          />
          <Table.Column<FtpAccount>
            key="actions"
            title={t("ftpaccounts.actions")}
            render={(_, row) => (
              <Space>
                <RowActionButton
                  icon={<KeyOutlined />}
                  onClick={() => setPwTarget(row)}
                >
                  {t("ftpaccounts.password")}
                </RowActionButton>
                <RowActionButton
                  icon={<CloudServerOutlined />}
                  onClick={() => handleToggle(row, { is_enabled: !row.is_enabled })}
                >
                  {row.is_enabled ? t("ftpaccounts.disable") : t("ftpaccounts.enable")}
                </RowActionButton>
                <Popconfirm
                  title={t("ftpaccounts.delete_confirm", { name: row.username })}
                  onConfirm={() => handleDelete(row)}
                >
                  <RowActionButton danger icon={<DeleteOutlined />}>
                    {t("ftpaccounts.delete")}
                  </RowActionButton>
                </Popconfirm>
              </Space>
            )}
          />
        </Table>
      </Card>

      <Drawer
        title={t("ftpaccounts.add_account")}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        width={isDesktop ? 520 : undefined}
        placement="right"
        destroyOnClose
      >
        <Form
          form={form}
          layout="vertical"
          onFinish={handleCreate}
          initialValues={{ ftp_access: false, isolated: isoAvailable }}
        >
          <Form.Item
            label={t("ftpaccounts.label")}
            name="label"
            tooltip={t("ftpaccounts.label_tooltip", { prefix: username ? `${username}_` : "" })}
            rules={[
              { required: true },
              {
                pattern: /^[a-z0-9][a-z0-9_]{0,19}$/,
                message: t("ftpaccounts.label_rule"),
              },
            ]}
          >
            <Input addonBefore={username ? `${username}_` : undefined} placeholder="deploy" />
          </Form.Item>
          <Form.Item
            label={t("ftpaccounts.directory")}
            name="home_rel"
            tooltip={t("ftpaccounts.directory_tooltip")}
            rules={[
              {
                validator: (_, v: string) =>
                  !v || (!v.includes("..") && !/[\s"'\\:]/.test(v))
                    ? Promise.resolve()
                    : Promise.reject(new Error(t("ftpaccounts.directory_rel_rule"))),
              },
            ]}
          >
            <Input
              addonBefore={`${homePrefix}/`}
              placeholder="example.com/public_html"
            />
          </Form.Item>
          <Form.Item
            label={t("ftpaccounts.password")}
            name="password"
            rules={[{ required: true, min: 12, max: 128 }]}
          >
            <PasswordInput autoComplete="new-password" generatorLength={20} />
          </Form.Item>
          <Form.Item
            label={t("ftpaccounts.isolated_label")}
            name="isolated"
            valuePropName="checked"
            tooltip={t("ftpaccounts.isolated_tooltip")}
            extra={
              !isoAvailable
                ? t("ftpaccounts.isolated_unavailable")
                : undefined
            }
          >
            <Switch disabled={!isoAvailable} />
          </Form.Item>
          <Form.Item
            noStyle
            shouldUpdate={(prev, cur) => prev.isolated !== cur.isolated}
          >
            {({ getFieldValue }) =>
              getFieldValue("isolated") !== false ? (
                <Form.Item
                  label={t("ftpaccounts.quota_label")}
                  name="quota_mb"
                  tooltip={t("ftpaccounts.quota_tooltip")}
                  rules={[
                    {
                      required: true,
                      type: "number",
                      min: 1,
                      message: t("ftpaccounts.quota_rule"),
                    },
                  ]}
                >
                  <InputNumber
                    min={1}
                    style={{ width: "100%" }}
                    addonAfter="MB"
                    placeholder="500"
                  />
                </Form.Item>
              ) : null
            }
          </Form.Item>
          <Form.Item
            label={t("ftpaccounts.ftp_access_label")}
            name="ftp_access"
            valuePropName="checked"
            tooltip={
              caps?.ftp_server_enabled
                ? t("ftpaccounts.ftp_access_tooltip")
                : t("ftpaccounts.ftp_disabled_tooltip")
            }
          >
            <Switch disabled={!caps?.ftp_server_enabled} />
          </Form.Item>
          <StandardDrawerFooter
            primaryText={t("ftpaccounts.add_account")}
            onPrimary={() => form.submit()}
            primaryLoading={saving}
            onCancel={() => setDrawerOpen(false)}
          />
        </Form>
      </Drawer>

      <Modal
        title={t("ftpaccounts.reset_password_for", { name: pwTarget?.username ?? "" })}
        open={!!pwTarget}
        onCancel={() => setPwTarget(null)}
        onOk={() => pwForm.submit()}
        confirmLoading={saving}
        destroyOnClose
      >
        <Form form={pwForm} layout="vertical" onFinish={handlePassword}>
          <Form.Item
            label={t("ftpaccounts.new_password")}
            name="password"
            rules={[{ required: true, min: 12, max: 128 }]}
          >
            <PasswordInput autoComplete="new-password" generatorLength={20} />
          </Form.Item>
        </Form>
      </Modal>
    </Flex>
  );
};

// extractApiError pulls the API's {error, detail} envelope out of an axios
// failure; falls back to the caller's generic message.
function extractApiError(err: unknown, fallback: string): string {
  if (typeof err === "object" && err !== null) {
    const resp = (err as { response?: { data?: { detail?: string; error?: string } } }).response;
    if (resp?.data?.detail) return resp.data.detail;
    if (resp?.data?.error) return resp.data.error;
  }
  return fallback;
}
