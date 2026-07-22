import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { shortDateTime } from "../../../utils/datetime";
import { StandardDrawerFooter } from "../../../components/StandardActionFooter";
import {
  Alert,
  Button,
  Card,
  Drawer,
  Flex,
  Grid,
  Space,
  Table,
  Typography,
  Modal,
  Form,
  Input,
  message,
  Popconfirm,
  Tooltip,
} from "antd";
import {
  PlusSquareOutlined,
  DeleteOutlined,
  KeyOutlined,
  CopyOutlined,
  DownloadOutlined,
  CodeOutlined,
} from "@icons";
import { RowActionButton } from "../../../components/RowActionButton";
import { getKeys as getEd25519SSHKeys } from "micro-key-producer/ssh.js";
import {
  listSSHKeys,
  createSSHKey,
  deleteSSHKey,
  getSSHConnection,
  type SSHKey,
} from "../../../apiClient";
import { useQuery } from "@tanstack/react-query";

const SSH_KEY_PREFIXES = ["ssh-rsa ", "ssh-ed25519 ", "ecdsa-sha2-"];

// generateEd25519Keypair runs entirely in the browser: 32 bytes from
// Web Crypto feed micro-key-producer's OpenSSH encoder (the successor
// to ed25519-keygen), which returns the public key in authorized_keys
// format and the private key in OpenSSH PEM format. The private key
// never transits the network — only the public half is POSTed via the
// normal /ssh-keys endpoint.
function generateEd25519Keypair(comment: string) {
  const seed = new Uint8Array(32);
  crypto.getRandomValues(seed);
  return getEd25519SSHKeys(seed, comment || "jabali");
}

export const UserSSHKeysPage = () => {
  const { t } = useTranslation();
  const [form] = Form.useForm();
  const [genForm] = Form.useForm();
  const [modalOpen, setModalOpen] = useState(false);
  const [genOpen, setGenOpen] = useState(false);
  const [generating, setGenerating] = useState(false);
  // generatedPrivate is the plaintext OpenSSH private key we just made.
  // Held in React state only long enough to show it to the user once;
  // cleared when the result modal closes.
  const [generatedPrivate, setGeneratedPrivate] = useState<string | null>(null);
  const [generatedName, setGeneratedName] = useState<string>("");
  const [loading, setLoading] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  // Fetch SSH keys using react-query
  const { data: listResponse = { items: [] }, isLoading, refetch } = useQuery({
    queryKey: ["ssh-keys"],
    queryFn: async () => listSSHKeys(),
  });

  const keys = listResponse.items || [];
  const [search, setSearch] = useState("");
  const filteredKeys = useMemo(() => {
    if (!search) return keys;
    const needle = search.toLowerCase();
    return keys.filter(
      (k: SSHKey) =>
        k.name.toLowerCase().includes(needle) ||
        k.fingerprint.toLowerCase().includes(needle),
    );
  }, [keys, search]);

  // Connection details — separate query so a 409 "no_linux_account" (admins
  // without a shell user) just hides the card instead of blocking the page.
  const { data: conn, isLoading: connLoading } = useQuery({
    queryKey: ["ssh-connection"],
    queryFn: getSSHConnection,
    retry: false,
  });

  // Validate that public key starts with a known prefix
  const validatePublicKey = (value: string): string | undefined => {
    if (!value) return undefined;
    const hasValidPrefix = SSH_KEY_PREFIXES.some((prefix) => value.trim().startsWith(prefix));
    if (!hasValidPrefix) {
      return "Public key must start with ssh-rsa, ssh-ed25519, or ecdsa-sha2-";
    }
    return undefined;
  };

  const handleAddKey = async (values: { name: string; public_key: string }) => {
    setLoading(true);
    try {
      // Client-side validation
      const keyError = validatePublicKey(values.public_key);
      if (keyError) {
        message.error(keyError);
        return;
      }

      await createSSHKey({
        name: values.name,
        public_key: values.public_key,
      });

      message.success("SSH key added successfully");
      form.resetFields();
      setModalOpen(false);
      refetch();
    } catch (error: unknown) {
      const err = error as any;
      if (err?.response?.data?.error === "invalid_key") {
        message.error(
          "The public key could not be parsed. Make sure you paste the line from ~/.ssh/id_ed25519.pub (or similar), not a private key.",
        );
      } else if (err?.response?.data?.error === "duplicate_key") {
        message.error("This key is already registered.");
      } else {
        const msg = err?.message ?? "Failed to add SSH key";
        message.error(msg);
      }
    } finally {
      setLoading(false);
    }
  };

  const handleGenerate = async (values: { name: string; comment?: string }) => {
    setGenerating(true);
    try {
      const { publicKey, privateKey } = generateEd25519Keypair(values.comment ?? "");
      await createSSHKey({ name: values.name, public_key: publicKey });
      setGeneratedPrivate(privateKey);
      setGeneratedName(values.name);
      setGenOpen(false);
      genForm.resetFields();
      message.success("SSH key generated — save the private key now");
      refetch();
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } }; message?: string };
      if (err?.response?.data?.error === "duplicate_key") {
        // Extraordinarily unlikely with 32 random bytes, but if the
        // user re-clicks fast enough we could race their own list.
        message.error("This key is already registered — try again.");
      } else {
        message.error(err?.message ?? "Failed to generate SSH key");
      }
    } finally {
      setGenerating(false);
    }
  };

  const downloadPrivateKey = () => {
    if (!generatedPrivate) return;
    // .pem is the conventional extension for OpenSSH's armored private
    // key; ssh/sftp clients accept either .pem or no extension.
    const blob = new Blob([generatedPrivate], { type: "application/x-pem-file" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${(generatedName || "id_ed25519").replace(/[^a-zA-Z0-9_-]/g, "_")}.pem`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  };

  const copyPrivateKey = async () => {
    if (!generatedPrivate) return;
    try {
      await navigator.clipboard.writeText(generatedPrivate);
      message.success("Private key copied to clipboard");
    } catch {
      message.error("Copy failed — select and copy manually");
    }
  };

  const handleDelete = async (key: SSHKey) => {
    setDeletingId(key.id);
    try {
      await deleteSSHKey(key.id);
      message.success("SSH key deleted successfully");
      refetch();
    } catch (error) {
      const msg = (error as any)?.message ?? "Failed to delete SSH key";
      message.error(msg);
    } finally {
      setDeletingId(null);
    }
  };

  const truncateFingerprint = (fp: string): string => {
    if (fp.length <= 16) return fp;
    return fp.substring(0, 16) + "…";
  };

  return (
    <div>
      <Flex
        wrap
        gap="middle"
        justify="space-between"
        align="center"
        style={{ marginBottom: 16 }}
      >
        <Typography.Title level={3} style={{ margin: 0, wordBreak: "break-word" }}>
          <KeyOutlined /> SSH Keys
        </Typography.Title>
        <Space wrap>
          <Button
            icon={<KeyOutlined />}
            onClick={() => setGenOpen(true)}
          >
            Generate Key
          </Button>
          <Button
            type="primary"
            icon={<PlusSquareOutlined />}
            onClick={() => setModalOpen(true)}
          >
            Add Key
          </Button>
        </Space>
      </Flex>

      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        title={<strong>SSH & SFTP Access</strong>}
        description={t("usersshkeyspage.connect_to_your_server_securely_using_ssh_fo")}
      />

      {conn && (
        <Card
          style={{ marginBottom: 16 }}
          title={
            <Space>
              <CodeOutlined />
              <span>Connection Details</span>
            </Space>
          }
        >
          <Space wrap size="large" style={{ width: "100%" }}>
            <span>
              <Typography.Text type="secondary">Host: </Typography.Text>
              <Typography.Text code>{conn.host}</Typography.Text>
            </span>
            <span>
              <Typography.Text type="secondary">Port: </Typography.Text>
              <Typography.Text code>{conn.port}</Typography.Text>
            </span>
            <span>
              <Typography.Text type="secondary">Username: </Typography.Text>
              <Typography.Text code>{conn.username}</Typography.Text>
            </span>
            <span>
              <Typography.Text type="secondary">Command: </Typography.Text>
              <Typography.Text code copyable={{ text: conn.command, tooltips: ["Copy", "Copied"] }}>
                {conn.command}
              </Typography.Text>
            </span>
          </Space>
        </Card>
      )}
      {connLoading && !conn && (
        <Card loading style={{ marginBottom: 16 }} />
      )}

      <Card>
        <Input.Search
          placeholder={t("usersshkeyspage.search_by_name_or_fingerprint")}
          allowClear
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          onSearch={(value) => setSearch(value.trim())}
          style={{ maxWidth: 360, marginBottom: 12 }}
        />
        <Table<SSHKey>
          dataSource={filteredKeys}
          loading={isLoading || deletingId !== null}
          rowKey="id"
          pagination={false}
          scroll={{ x: "max-content" }}
          columns={[
            {
              title: "Name",
              dataIndex: "name",
              sorter: (a, b) => a.name.localeCompare(b.name),
              defaultSortOrder: "ascend",
            },
            {
              title: "Fingerprint",
              dataIndex: "fingerprint",
              render: (fingerprint: string) => (
                <Tooltip title={fingerprint}>
                  <span style={{ fontFamily: "monospace" }}>
                    {truncateFingerprint(fingerprint)}
                  </span>
                </Tooltip>
              ),
            },
            {
              title: "Created",
              dataIndex: "created_at",
              sorter: (a, b) =>
                new Date(a.created_at).getTime() -
                new Date(b.created_at).getTime(),
              render: (date: string) => shortDateTime(date),
            },
            {
              title: "Actions",
              dataIndex: "actions",
              render: (_, record) => (
                <Popconfirm
                  title={t("usersshkeyspage.delete_ssh_key")}
                  description={t("usersshkeyspage.are_you_sure_this_revokes_sftp_access")}
                  onConfirm={() => handleDelete(record)}
                  okText={t("usersshkeyspage.yes")}
                  cancelText="No"
                >
                  <RowActionButton
                    danger
                    icon={<DeleteOutlined />}
                    loading={deletingId === record.id}
                    disabled={deletingId !== null && deletingId !== record.id}
                  >
                    Delete
                  </RowActionButton>
                </Popconfirm>
              ),
            },
          ]}
        />
      </Card>

      <Drawer
        title={t("usersshkeyspage.add_ssh_key")}
        open={modalOpen}
        onClose={() => {
          setModalOpen(false);
          form.resetFields();
        }}
        width={isDesktop ? 520 : undefined}
        placement="right"
        destroyOnClose
        footer={
          <StandardDrawerFooter
            formId="ssh-add-form"
            primaryText="Add Key"
            primaryLoading={loading}
            onCancel={() => {
              setModalOpen(false);
              form.resetFields();
            }}
          />
        }
      >
        <Form
          id="ssh-add-form"
          form={form}
          layout="vertical"
          onFinish={handleAddKey}
        >
          <Form.Item
            label={t("usersshkeyspage.name")}
            name="name"
            rules={[
              { required: true, message: "Please enter a name" },
              { max: 128, message: "Name must be 128 characters or less" },
            ]}
          >
            <Input placeholder="e.g., My Laptop" />
          </Form.Item>

          <Form.Item
            label={t("usersshkeyspage.public_key")}
            name="public_key"
            rules={[
              { required: true, message: "Please paste your public key" },
              {
                validator: (_, value) => {
                  const error = validatePublicKey(value);
                  return error ? Promise.reject(error) : Promise.resolve();
                },
              },
            ]}
          >
            <Input.TextArea
              placeholder="ssh-ed25519 AAAA... user@host"
              rows={4}
            />
          </Form.Item>

        </Form>
      </Drawer>

      {/* Generate Key — collects a name + optional comment, then creates
          an ed25519 keypair entirely in the browser via micro-key-producer
          and POSTs only the public half. The private key is shown once
          in the result modal below. */}
      <Drawer
        title={t("usersshkeyspage.generate_ssh_key")}
        open={genOpen}
        onClose={() => {
          setGenOpen(false);
          genForm.resetFields();
        }}
        width={isDesktop ? 520 : undefined}
        placement="right"
        destroyOnClose
        footer={
          <StandardDrawerFooter
            formId="ssh-gen-form"
            primaryText="Generate Keypair"
            primaryLoading={generating}
            onCancel={() => {
              setGenOpen(false);
              genForm.resetFields();
            }}
          />
        }
      >
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          Generates an Ed25519 keypair in your browser. The private key
          is shown once after creation — save it somewhere safe because
          we never see it and cannot recover it.
        </Typography.Paragraph>
        <Form id="ssh-gen-form" form={genForm} layout="vertical" onFinish={handleGenerate}>
          <Form.Item
            label={t("usersshkeyspage.name")}
            name="name"
            rules={[
              { required: true, message: "Please enter a name" },
              { max: 128, message: "Name must be 128 characters or less" },
            ]}
          >
            <Input placeholder="e.g., My Laptop" />
          </Form.Item>
          <Form.Item
            label={t("usersshkeyspage.comment_optional")}
            name="comment"
            extra="Embedded in the public key as a label; defaults to 'jabali'."
          >
            <Input placeholder="user@host" />
          </Form.Item>
        </Form>
      </Drawer>

      {/* Result modal — shown immediately after a successful generate.
          Shows the private key once with copy + download. Dismissing
          clears it from React state so it's not retained in memory. */}
      <Modal
        title={t("usersshkeyspage.save_your_private_key_now")}
        open={generatedPrivate !== null}
        onCancel={() => {
          setGeneratedPrivate(null);
          setGeneratedName("");
        }}
        footer={
          <Button type="primary" onClick={() => {
            setGeneratedPrivate(null);
            setGeneratedName("");
          }}>
            I've saved it
          </Button>
        }
        width={680}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          title={t("usersshkeyspage.this_is_the_only_time_the_private_key_will_b")}
          description={t("usersshkeyspage.copy_or_download_it_now_if_you_close_this_di")}
        />
        <Space style={{ marginBottom: 8 }}>
          <Button icon={<CopyOutlined />} onClick={copyPrivateKey}>
            Copy
          </Button>
          <Button icon={<DownloadOutlined />} onClick={downloadPrivateKey}>
            Download .pem
          </Button>
        </Space>
        <Input.TextArea
          readOnly
          value={generatedPrivate ?? ""}
          rows={14}
          style={{ fontFamily: "monospace" }}
          onFocus={(e) => e.currentTarget.select()}
        />
      </Modal>
    </div>
  );
};
