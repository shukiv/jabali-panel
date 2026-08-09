// M30.1 Destinations admin tab. Lists every backup_destinations row,
// drawer for create/edit, "Test" button verifies creds against the
// remote via `restic snapshots`.
//
// SFTP gets its own structured form with host/user/port/path inputs +
// an auth picker (SSH key dropdown / password). The dropdown reads
// /root/.ssh/ via the system-ssh-keys endpoint and offers a
// "Generate new key" inline action that calls the same endpoint with
// POST.
import { useTranslation } from "react-i18next";
import { Alert, Button, Drawer, Form, Input, InputNumber, Modal, Radio, Select, Space, Switch, Table, Tag, Typography, message } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { RowActions } from "../../../components/RowActions";
import {
  CheckCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  FlaskConicalOutlined,
  KeyOutlined,
} from "@icons";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";

type BackendKind = "local" | "sftp" | "s3" | "b2" | "azure" | "gcs" | "rest";

interface BackupDestinationExtraOptions {
  sftp?: {
    host: string;
    user: string;
    port?: number;
    path: string;
    auth: "key" | "password";
    key_path?: string;
  };
}

interface BackupDestination {
  id: string;
  name: string;
  kind: BackendKind;
  url: string;
  has_credentials: boolean;
  credentials_keys_mask?: string[];
  extra_options?: BackupDestinationExtraOptions;
  enabled: boolean;
  created_at: string;
  updated_at: string;
  /** M30.2: ISO timestamp of last per-destination restic password
   *  rotation. Null when the row is still on the legacy shared
   *  password file. */
  password_rotated_at?: string | null;
}

interface SSHKeyEntry {
  name: string;
  path: string;
  pubkey_path: string;
  pubkey: string;
  has_passphrase: boolean;
}

const KIND_OPTIONS: Array<{ value: BackendKind; label: string }> = [
  { value: "local", label: "Local (this server)" },
  { value: "sftp", label: "SFTP" },
  { value: "s3", label: "S3-compatible (AWS, MinIO, Wasabi, R2)" },
  { value: "b2", label: "Backblaze B2 (native)" },
  { value: "azure", label: "Azure Blob Storage" },
  { value: "gcs", label: "Google Cloud Storage" },
  { value: "rest", label: "Restic REST server" },
];

const URL_HINT: Record<BackendKind, string> = {
  local: "/var/lib/jabali-backups/repo",
  sftp: "(composed automatically from host + user + path)",
  s3: "s3:s3.amazonaws.com/bucket/path",
  b2: "b2:bucket-name:path",
  azure: "azure:container:/path",
  gcs: "gs:bucket-name:/path",
  rest: "rest:https://restic-server.example.com/repo/",
};

const ENV_HINT: Record<BackendKind, string> = {
  local: "(none)",
  sftp: "(handled by SSH key / password fields)",
  s3: "AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY",
  b2: "B2_ACCOUNT_ID, B2_ACCOUNT_KEY",
  azure: "AZURE_ACCOUNT_NAME, AZURE_ACCOUNT_KEY",
  gcs: "GOOGLE_APPLICATION_CREDENTIALS (path)",
  rest: "(basic auth via URL or cert)",
};

interface DestinationDrawerProps {
  open: boolean;
  editing: BackupDestination | null;
  onClose: () => void;
  onSaved: () => void;
}

function DestinationDrawer({ open, editing, onClose, onSaved }: DestinationDrawerProps) {
  const [form] = Form.useForm();
  const [busy, setBusy] = useState(false);
  const [sshKeys, setSshKeys] = useState<SSHKeyEntry[]>([]);
  const [keysLoading, setKeysLoading] = useState(false);
  const [generateOpen, setGenerateOpen] = useState(false);

  const reloadKeys = async () => {
    setKeysLoading(true);
    try {
      const resp = await apiClient.get<{ data: SSHKeyEntry[] }>(
        "/admin/system/ssh-keys",
      );
      setSshKeys(resp.data.data ?? []);
    } catch (err) {
      message.error(extractApiError(err, "ssh-key list failed"));
    } finally {
      setKeysLoading(false);
    }
  };

  useEffect(() => {
    if (open) {
      form.resetFields();
      void reloadKeys();
      if (editing) {
        const sftp = editing.extra_options?.sftp;
        form.setFieldsValue({
          name: editing.name,
          kind: editing.kind,
          url: editing.url,
          enabled: editing.enabled,
          sftp_host: sftp?.host,
          sftp_user: sftp?.user,
          sftp_port: sftp?.port,
          sftp_path: sftp?.path,
          sftp_auth: sftp?.auth ?? "key",
          sftp_key_path: sftp?.key_path,
        });
      } else {
        form.setFieldsValue({
          kind: "sftp",
          enabled: true,
          sftp_port: 22,
          sftp_auth: "key",
        });
      }
    }
  }, [open, editing, form]);

  const handleSave = async () => {
    let values;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    setBusy(true);
    try {
      const credentialsEnv = parseEnvBlock(values.credentials_env_block || "");
      const body: Record<string, unknown> = {
        name: values.name,
        kind: values.kind,
        enabled: values.enabled,
      };
      if (values.kind === "sftp") {
        body.sftp = {
          host: values.sftp_host,
          user: values.sftp_user,
          port: values.sftp_port ?? 22,
          path: values.sftp_path,
          auth: values.sftp_auth,
          key_path: values.sftp_auth === "key" ? values.sftp_key_path || "" : "",
        };
        if (values.sftp_auth === "password") {
          if (!values.sftp_password && !editing?.has_credentials) {
            message.error("password is required for SFTP password auth");
            setBusy(false);
            return;
          }
          if (values.sftp_password) body.sftp_password = values.sftp_password;
        }
      } else {
        body.url = values.url;
        if (Object.keys(credentialsEnv).length > 0) {
          body.credentials_env = credentialsEnv;
        }
      }
      let savedID: string | undefined;
      if (editing) {
        await apiClient.patch(`/admin/backup-destinations/${editing.id}`, body);
        savedID = editing.id;
      } else {
        const created = await apiClient.post<{ id?: string }>(
          "/admin/backup-destinations",
          body,
        );
        savedID = created.data?.id;
      }
      // Local destinations skip the SSH/restic round-trip — agent
      // returns ok unconditionally. Remote backends get tested so the
      // operator finds out about bad creds / unreachable hosts BEFORE
      // the first scheduled backup runs.
      if (savedID && values.kind !== "local") {
        try {
          const resp = await apiClient.post<{ status: string; detail?: string; stderr?: string }>(
            `/admin/backup-destinations/${savedID}/test`,
            {},
          );
          message.success(
            resp.data.detail ? `Saved + tested OK — ${resp.data.detail}` : "Saved + tested OK",
          );
        } catch (testErr) {
          feedback.notification.error({
            message: editing ? "Saved, but test failed" : "Created, but test failed",
            description: extractApiError(testErr, "Test failed"),
            duration: 0,
          });
          onSaved();
          return;
        }
      } else {
        message.success(editing ? "Destination updated" : "Destination created");
      }
      onSaved();
    } catch (err) {
      message.error(extractApiError(err, "Save failed"));
    } finally {
      setBusy(false);
    }
  };

  const kindWatch: BackendKind = Form.useWatch("kind", form) || "sftp";
  const sftpAuthWatch: "key" | "password" = Form.useWatch("sftp_auth", form) || "key";

  return (
    <Drawer
      title={editing ? `Edit destination — ${editing.name}` : "New destination"}
      width={560}
      open={open}
      onClose={onClose}
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="primary" loading={busy} onClick={handleSave}>
            Save
          </Button>
        </Space>
      }
    >
      <Form form={form} layout="vertical">
        <Form.Item name="name" label="Name" rules={[{ required: true }]}>
          <Input placeholder="offsite-s3" />
        </Form.Item>
        <Form.Item name="kind" label="Backend" rules={[{ required: true }]}>
          <Select options={KIND_OPTIONS} disabled={!!editing} />
        </Form.Item>

        {kindWatch === "sftp" ? (
          <SFTPFields
            sshKeys={sshKeys}
            keysLoading={keysLoading}
            sftpAuthWatch={sftpAuthWatch}
            editing={editing}
            onGenerateKeyOpen={() => setGenerateOpen(true)}
          />
        ) : (
          <>
            <Form.Item
              name="url"
              label="Restic URL"
              rules={[{ required: true }]}
              extra={`Format: ${URL_HINT[kindWatch]}`}
            >
              <Input placeholder={URL_HINT[kindWatch]} />
            </Form.Item>
            <Form.Item
              name="credentials_env_block"
              label="Credentials (KEY=VALUE per line)"
              extra={`Required env vars: ${ENV_HINT[kindWatch]}`}
            >
              <Input.TextArea
                rows={5}
                placeholder={
                  kindWatch === "s3"
                    ? "AWS_ACCESS_KEY_ID=AKIA…\nAWS_SECRET_ACCESS_KEY=…"
                    : ENV_HINT[kindWatch]
                }
              />
            </Form.Item>
            {editing?.has_credentials && (
              <Typography.Text type="secondary">
                Stored credentials: {editing.credentials_keys_mask?.join(", ") || "(redacted)"}.
                Leave the field blank to keep them; fill it to overwrite.
              </Typography.Text>
            )}
          </>
        )}

        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
          <Switch />
        </Form.Item>
      </Form>

      <GenerateKeyModal
        open={generateOpen}
        onClose={() => setGenerateOpen(false)}
        onGenerated={(entry) => {
          setSshKeys((prev) => [...prev, entry]);
          form.setFieldsValue({ sftp_key_path: entry.path });
          setGenerateOpen(false);
        }}
      />
    </Drawer>
  );
}

interface SFTPFieldsProps {
  sshKeys: SSHKeyEntry[];
  keysLoading: boolean;
  sftpAuthWatch: "key" | "password";
  editing: BackupDestination | null;
  onGenerateKeyOpen: () => void;
}

function SFTPFields({
  sshKeys,
  keysLoading,
  sftpAuthWatch,
  editing,
  onGenerateKeyOpen,
}: SFTPFieldsProps) {
  const usableKeys = sshKeys.filter((k) => !k.has_passphrase);
  const passphraseKeys = sshKeys.filter((k) => k.has_passphrase);

  return (
    <>
      <Space.Compact block>
        <Form.Item
          name="sftp_user"
          label="User"
          rules={[{ required: true }]}
          style={{ flex: 1 }}
        >
          <Input placeholder="bob" />
        </Form.Item>
        <Form.Item
          name="sftp_host"
          label="Host"
          rules={[{ required: true }]}
          style={{ flex: 2 }}
        >
          <Input placeholder="backup.example.com" />
        </Form.Item>
        <Form.Item name="sftp_port" label="Port" style={{ width: 100 }}>
          <InputNumber min={1} max={65535} placeholder="22" style={{ width: "100%" }} />
        </Form.Item>
      </Space.Compact>
      <Form.Item
        name="sftp_path"
        label="Repo path on remote"
        rules={[{ required: true }]}
        extra="Absolute path. Restic init will create it if missing."
      >
        <Input placeholder="/srv/backups/jabali-mx" />
      </Form.Item>

      <Form.Item name="sftp_auth" label="Authentication" rules={[{ required: true }]}>
        <Radio.Group>
          <Radio.Button value="key">SSH key</Radio.Button>
          <Radio.Button value="password">Password</Radio.Button>
        </Radio.Group>
      </Form.Item>

      {sftpAuthWatch === "key" ? (
        <>
          <Form.Item
            name="sftp_key_path"
            label="SSH private key"
            extra={
              usableKeys.length === 0
                ? "No usable keys found in /root/.ssh. Generate one below."
                : "Pick from /root/.ssh/. Only passphrase-less keys work with restic (BatchMode)."
            }
          >
            <Select
              loading={keysLoading}
              placeholder="(default — uses ssh-agent / id_rsa / id_ed25519)"
              allowClear
              options={[
                ...usableKeys.map((k) => ({
                  value: k.path,
                  label: k.name,
                })),
                ...passphraseKeys.map((k) => ({
                  value: k.path,
                  label: `${k.name} — has passphrase, will fail BatchMode`,
                  disabled: true,
                })),
              ]}
            />
          </Form.Item>
          <Space style={{ marginBottom: 16 }}>
            <Button onClick={onGenerateKeyOpen}>Generate new key</Button>
          </Space>
        </>
      ) : (
        <>
          <Form.Item
            name="sftp_password"
            label="SSH password"
            extra={
              editing?.has_credentials
                ? "Leave blank to keep the stored password. Fill to overwrite."
                : "Stored encrypted at rest in /etc/jabali-panel/restic-remotes/<id>.env (root:root 0600)."
            }
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 12 }}
            message="Password auth requires sshpass on the host. install.sh provisions it."
          />
        </>
      )}
    </>
  );
}

interface GenerateKeyModalProps {
  open: boolean;
  onClose: () => void;
  onGenerated: (entry: SSHKeyEntry) => void;
}

function GenerateKeyModal({ open, onClose, onGenerated }: GenerateKeyModalProps) {
  const [form] = Form.useForm();
  const [busy, setBusy] = useState(false);
  const [pubkey, setPubkey] = useState<string>("");

  useEffect(() => {
    if (open) {
      form.resetFields();
      form.setFieldsValue({ name: "id_jabali_offsite", type: "ed25519" });
      setPubkey("");
    }
  }, [open, form]);

  const handleGenerate = async () => {
    let values;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    setBusy(true);
    try {
      const resp = await apiClient.post<{
        name: string;
        path: string;
        pubkey_path: string;
        pubkey: string;
      }>("/admin/system/ssh-keys", values);
      setPubkey(resp.data.pubkey);
      onGenerated({
        name: resp.data.name,
        path: resp.data.path,
        pubkey_path: resp.data.pubkey_path,
        pubkey: resp.data.pubkey,
        has_passphrase: false,
      });
    } catch (err) {
      message.error(extractApiError(err, "key generation failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title="Generate SSH key"
      open={open}
      onCancel={onClose}
      footer={
        pubkey ? (
          <Button type="primary" onClick={onClose}>
            Done
          </Button>
        ) : (
          <Space>
            <Button onClick={onClose}>Cancel</Button>
            <Button type="primary" loading={busy} onClick={handleGenerate}>
              Generate
            </Button>
          </Space>
        )
      }
    >
      {!pubkey ? (
        <Form form={form} layout="vertical">
          <Form.Item
            name="name"
            label="Key name"
            rules={[
              { required: true },
              {
                pattern: /^id_[A-Za-z0-9_]+$/,
                message: "Name must start with id_ and contain only letters, digits, _",
              },
            ]}
            extra="Stored as /root/.ssh/<name> (private) + <name>.pub."
          >
            <Input />
          </Form.Item>
          <Form.Item name="type" label="Type" rules={[{ required: true }]}>
            <Radio.Group>
              <Radio.Button value="ed25519">ed25519 (recommended)</Radio.Button>
              <Radio.Button value="rsa">rsa-4096</Radio.Button>
            </Radio.Group>
          </Form.Item>
        </Form>
      ) : (
        <>
          <Alert
            type="success"
            showIcon
            style={{ marginBottom: 12 }}
            message="Key generated"
            description={
              <Typography.Text>
                Add the public key below to the remote's <code>~/.ssh/authorized_keys</code> before
                saving the destination.
              </Typography.Text>
            }
          />
          <Input.TextArea
            rows={4}
            value={pubkey}
            readOnly
            onClick={(e) => (e.target as HTMLTextAreaElement).select()}
          />
        </>
      )}
    </Modal>
  );
}

function parseEnvBlock(block: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const raw of block.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    const key = line.slice(0, eq).trim();
    const value = line.slice(eq + 1).trim();
    if (key && value) out[key] = value;
  }
  return out;
}

export function DestinationsTab() {
  const { t } = useTranslation();
  const [rows, setRows] = useState<BackupDestination[]>([]);
  const [loading, setLoading] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<BackupDestination | null>(null);
  const [rotatingId, setRotatingId] = useState<string | null>(null);

  const reload = async () => {
    setLoading(true);
    try {
      const resp = await apiClient.get<{ data: BackupDestination[] }>(
        "/admin/backup-destinations",
      );
      setRows(resp.data.data ?? []);
    } catch (err) {
      message.error(extractApiError(err, "Load failed"));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void reload();
  }, []);

  const handleDelete = async (row: BackupDestination) => {
    feedback.modal.confirm({
      title: `Delete destination "${row.name}"?`,
      content:
        "Existing backups copied to this destination remain on the remote (panel does not " +
        "auto-purge remote snapshots). Schedules linked to this destination will simply " +
        "stop copying to it.",
      okType: "danger",
      onOk: async () => {
        try {
          await apiClient.delete(`/admin/backup-destinations/${row.id}`);
          message.success(`Deleted ${row.name}`);
          void reload();
        } catch (err) {
          message.error(extractApiError(err, "Delete failed"));
        }
      },
    });
  };

  const handleRotatePassword = async (row: BackupDestination) => {
    feedback.modal.confirm({
      title: `Rotate restic password for "${row.name}"?`,
      content: (
        <div>
          <p>
            A new 32-byte random password will be added to the restic
            repository, then the old key removed. The repository data
            is unchanged — only the unlock key rotates.
          </p>
          <p>
            <strong>Make sure you have an off-host record</strong> of the
            new password before closing the reveal dialog. Jabali stores
            the encrypted blob, but the plaintext is shown only once.
          </p>
        </div>
      ),
      okText: "Rotate",
      cancelText: "Cancel",
      onOk: async () => {
        setRotatingId(row.id);
        try {
          const resp = await apiClient.post<{
            status: string;
            password: string;
            password_rotated_at: string;
          }>(`/admin/backup-destinations/${row.id}/rotate-password`, {});
          feedback.modal.info({
            title: `New restic password — ${row.name}`,
            width: 560,
            content: (
              <div>
                <p>Copy and store this password before closing — it is shown only once.</p>
                <Input.TextArea
                  readOnly
                  autoSize
                  value={resp.data.password}
                  style={{ fontFamily: "monospace", marginBottom: 8 }}
                />
                <Button
                  size="small"
                  onClick={() => {
                    void navigator.clipboard.writeText(resp.data.password);
                    message.success("Copied to clipboard");
                  }}
                >
                  Copy
                </Button>
              </div>
            ),
          });
          await reload();
        } catch (err) {
          message.error(extractApiError(err, "Rotate failed"));
        } finally {
          setRotatingId(null);
        }
      },
    });
  };

  const handleTest = async (row: BackupDestination) => {
    const hide = message.loading(`Testing ${row.name}…`, 0);
    try {
      const resp = await apiClient.post<{ status: string; detail?: string }>(
        `/admin/backup-destinations/${row.id}/test`,
        {},
      );
      hide();
      const detail = resp.data.detail;
      message.success(detail ? `OK — ${detail}` : "Connection OK");
    } catch (err) {
      hide();
      message.error(extractApiError(err, "Test failed"));
    }
  };

  return (
    <>
      <Space style={{ marginBottom: 12 }}>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditing(null);
            setDrawerOpen(true);
          }}
        >
          New destination
        </Button>
      </Space>
      <Table<BackupDestination>
        rowKey="id"
        loading={loading}
        dataSource={rows}
        pagination={false}
        scroll={{ x: "max-content" }}
      >
        <Table.Column dataIndex="name" title={t("destinationstab.name")} />
        <Table.Column
          dataIndex="kind"
          title={t("destinationstab.backend")}
          render={(k: string) => <Tag>{k.toUpperCase()}</Tag>}
        />
        <Table.Column dataIndex="url" title={t("destinationstab.url")} />
        <Table.Column
          dataIndex="enabled"
          title={t("destinationstab.enabled")}
          render={(v: boolean) => (v ? <Tag color="green">yes</Tag> : <Tag>no</Tag>)}
        />
        <Table.Column
          dataIndex="has_credentials"
          title={t("destinationstab.credentials")}
          render={(v: boolean) =>
            v ? <CheckCircleOutlined style={{ color: "#52c41a" }} /> : "—"
          }
        />
        <Table.Column<BackupDestination>
          title={t("destinationstab.actions")}
          render={(_, row) => (
            <RowActions
              actions={[
                { key: "test", label: "Test", icon: <FlaskConicalOutlined />, onClick: () => handleTest(row) },
                { key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => { setEditing(row); setDrawerOpen(true); } },
                { key: "rotate", label: "Rotate password", icon: <KeyOutlined />, loading: rotatingId === row.id, onClick: () => handleRotatePassword(row) },
                { key: "delete", label: "Delete", icon: <DeleteOutlined />, danger: true, onClick: () => handleDelete(row), confirm: { title: "Delete this destination?", okText: "Delete" } },
              ]}
            />
          )}
        />
      </Table>
      <DestinationDrawer
        open={drawerOpen}
        editing={editing}
        onClose={() => setDrawerOpen(false)}
        onSaved={() => {
          setDrawerOpen(false);
          void reload();
        }}
      />
    </>
  );
}
