// CreateMigrationDrawer — operator-driven 'New Migration' wizard.
// Step 1: pick source kind / host / user and create the job.
// Step 2+: source-specific drive steps (upload / pull / import) — no CLI needed.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Alert,
  Button,
  Drawer,
  Form,
  Input,
  InputNumber,
  Card,
  Space,
  Steps,
  Tabs,
  Typography,
  Upload,
  message,
} from "antd";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { UploadOutlined } from "../../../icons";

// ─── types ────────────────────────────────────────────────────────────────────

type CreateInput = {
  source_kind: string;
  source_host: string;
  source_user: string;
  source_port?: number;
  source_path?: string;
};

type MigrationJob = {
  id: string;
  source_kind: string;
  source_host: string;
  source_user: string;
  source_port?: number;
  state: string;
};

type SecretsInput = { ssh_password?: string; ssh_private_key?: string; plugin_token?: string };
type PullInput = { ssh_user: string };
type ImportInput = {
  target_user: string;
  target_email: string;
  target_password: string;
  target_package_id: string;
};

// 'done' is shared terminal step for all flows
type CPanelStep = "secrets" | "pull" | "import" | "done";
type WHMStep = "tarball" | "import" | "done";
type DriveStep = CPanelStep | WHMStep;

// ─── source options ────────────────────────────────────────────────────────────

const SOURCE_DESC: Record<string, string> = {
  cpanel: "Live SSH or uploaded pkgacct backup — full account, websites, DBs, mail, DNS, SSL",
  whm_pkgacct: "Uploaded WHM Full Backup / pkgacct tarball",
  directadmin: "Live SSH or backup_user tarball — domains, users, DBs, mail, DNS, SSL",
  hestiacp: "Live SSH or v-backup-user tarball — web, mail, DNS, databases, cron",
  cloudpanel: "Live SSH — web sites, PHP, databases, cron, SSH keys (no mail)",
  cyberpanel: "Live SSH — web sites, databases, cron, SSH keys, DNS, mail",
  plesk: "Live SSH — subscriptions, WordPress, DBs (streamed), mail, DNS",
  wordpress_ssh: "Cloudways / VPS / generic SSH — single WP site (files + DB + wp-config rewrite)",
  wordpress_plugin: "No SSH — jabali-migrator plugin on the source pushes over a token-authed API",
};

// SourceCards — a form-bound card grid (GH #665 mockup 02) replacing the select.
function SourceCards({ value, onChange }: { value?: string; onChange?: (v: string) => void }) {
  return (
    <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
      {SOURCE_OPTIONS.map((o) => (
        <Card
          key={o.value}
          hoverable
          size="small"
          onClick={() => onChange?.(o.value)}
          style={{ borderColor: value === o.value ? "#1677ff" : undefined, borderWidth: value === o.value ? 2 : 1 }}
        >
          <Typography.Text strong>{o.label}</Typography.Text>
          <div>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {SOURCE_DESC[o.value] ?? ""}
            </Typography.Text>
          </div>
        </Card>
      ))}
    </div>
  );
}

const SOURCE_OPTIONS = [
  { value: "cpanel", label: "cPanel (live SSH source)" },
  { value: "whm_pkgacct", label: "WHM pkgacct (uploaded tarball)" },
  { value: "directadmin", label: "DirectAdmin (live SSH source)" },
  { value: "hestiacp", label: "HestiaCP (live SSH source)" },
  { value: "cloudpanel", label: "CloudPanel (live SSH source)" },
  { value: "cyberpanel", label: "CyberPanel (live SSH source)" },
  { value: "plesk", label: "Plesk (live SSH source)" },
];

// ─── sub-step components ───────────────────────────────────────────────────────

function SecretsStep({ jobId, kind, onDone }: { jobId: string; kind?: string; onDone: () => void }) {
  const [form] = Form.useForm<SecretsInput>();
  const [credKind, setCredKind] = useState<"password" | "key">("password");

  const mut = useMutation({
    mutationFn: async (vals: SecretsInput) => {
      await apiClient.post(`/admin/migrations/${jobId}/secrets`, vals);
    },
    onSuccess: () => {
      message.success("Credentials saved");
      onDone();
    },
    onError: (err: unknown) => {
      const detail = (err as { response?: { data?: { detail?: string } } })
        ?.response?.data?.detail;
      message.error(detail ?? "Failed to save credentials");
    },
  });

  if (kind === "wordpress_plugin") {
    return (
      <Form layout="vertical" onFinish={(v) => void mut.mutateAsync(v)}>
        <Alert
          type="info"
          showIcon
          message="Migration token from the jabali-migrator plugin"
          description="On the source site, install the jabali-migrator plugin (Tools → Jabali Migrator), generate a token, and paste it here. Jabali pulls the DB + files over the plugin's token-authed REST API. The token is stored encrypted and deleted at terminal state."
          style={{ marginBottom: 16 }}
        />
        <Form.Item name="plugin_token" label="Migration token" rules={[{ required: true, message: "Token required" }]}>
          <Input.Password placeholder="64-char token from the plugin" autoComplete="off" />
        </Form.Item>
        <Button type="primary" htmlType="submit" loading={mut.isPending}>
          Save token
        </Button>
      </Form>
    );
  }
  return (
    <Form form={form} layout="vertical" onFinish={(v) => void mut.mutateAsync(v)}>
      <Alert
        type="info"
        showIcon
        message="SSH credentials for the source server"
        description="Credentials are stored encrypted, used only to pull the account tarball, then permanently deleted when the job reaches a terminal state."
        style={{ marginBottom: 16 }}
      />
      <Tabs
        activeKey={credKind}
        onChange={(k) => {
          setCredKind(k as typeof credKind);
          form.resetFields();
        }}
        items={[
          {
            key: "password",
            label: "Password",
            children: (
              <Form.Item
                name="ssh_password"
                label="SSH password"
                rules={credKind === "password" ? [{ required: true, message: "Password required" }] : []}
              >
                <Input.Password placeholder="root password on source host" autoComplete="off" />
              </Form.Item>
            ),
          },
          {
            key: "key",
            label: "Private key",
            children: (
              <Form.Item
                name="ssh_private_key"
                label="PEM private key"
                rules={credKind === "key" ? [{ required: true, message: "Private key required" }] : []}
              >
                <Input.TextArea
                  rows={6}
                  placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
                  style={{ fontFamily: "monospace", fontSize: 12 }}
                />
              </Form.Item>
            ),
          },
        ]}
      />
      <Button type="primary" htmlType="submit" loading={mut.isPending}>
        Save credentials
      </Button>
      <TestConnection jobId={jobId} />
    </Form>
  );
}

// TestConnection (GH #665 mockup 03) — handshake the source, show detected
// counts/version or the error, before pulling.
function TestConnection({ jobId }: { jobId: string }) {
  const [res, setRes] = useState<Record<string, unknown> | null>(null);
  const mut = useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<Record<string, unknown>>(
        `/admin/migrations/${jobId}/test-connection`,
        {},
      );
      return data;
    },
    onSuccess: (d) => setRes(d),
    onError: (err: unknown) => {
      const detail = (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data;
      setRes({ ok: false, error: detail?.detail ?? detail?.error ?? "Connection failed" });
    },
  });
  return (
    <Space direction="vertical" style={{ width: "100%", marginTop: 12 }}>
      <Button onClick={() => void mut.mutateAsync()} loading={mut.isPending}>
        Test connection
      </Button>
      {res?.ok ? (
        <Alert
          type="success"
          showIcon
          message="Connection OK"
          description={
            <span>
              {String(res.panel ?? "")}
              {res.version ? ` ${res.version}` : ""}
              {res.accounts != null ? ` · ${res.accounts} accounts` : ""}
              {res.domains != null ? ` · ${res.domains} domains` : ""}
              {res.databases != null ? ` · ${res.databases} DBs` : ""}
              {res.bytes != null ? ` · ${(Number(res.bytes) / 1073741824).toFixed(2)} GB` : ""}
              {res.needs_update ? " · ⚠ source plugin outdated (update to 0.1.2+)" : ""}
            </span>
          }
        />
      ) : res ? (
        <Alert type="error" showIcon message="Connection failed" description={String(res.error ?? "")} />
      ) : null}
    </Space>
  );
}

function PullStep({ jobId, onDone }: { jobId: string; onDone: () => void }) {
  const [form] = Form.useForm<PullInput>();

  const mut = useMutation({
    mutationFn: async (vals: PullInput) => {
      await apiClient.post(`/admin/migrations/${jobId}/pull-source`, vals);
    },
    onSuccess: () => {
      message.success("Pull started — running in the background");
      onDone();
    },
    onError: (err: unknown) => {
      const detail = (err as { response?: { data?: { detail?: string } } })
        ?.response?.data?.detail;
      message.error(detail ?? "Failed to start pull");
    },
  });

  return (
    <Form
      form={form}
      layout="vertical"
      onFinish={(v) => void mut.mutateAsync(v)}
      initialValues={{ ssh_user: "root" }}
    >
      <Alert
        type="warning"
        showIcon
        message="Pre-flight — review before you start"
        description="The source is left untouched (read-only). DNS is NOT switched automatically — Jabali imports the zones and prepares suggested records. Watch the job's progress timeline once it starts; you can retry a failed stage."
        style={{ marginBottom: 12 }}
      />
      <Alert
        type="info"
        showIcon
        message="Scan source & prepare migration"
        description="SSH-connects to the source, packages the account(s), transfers to staging, then imports. Progress shows on the job's stage timeline."
        style={{ marginBottom: 16 }}
      />
      <Form.Item
        name="ssh_user"
        label="SSH user"
        tooltip="Login used to connect to the source server. Usually 'root'."
      >
        <Input placeholder="root" />
      </Form.Item>
      <Button type="primary" htmlType="submit" loading={mut.isPending}>
        Start migration
      </Button>
    </Form>
  );
}

function TarballStep({ jobId, onDone }: { jobId: string; onDone: () => void }) {
  const [uploading, setUploading] = useState(false);

  const doUpload = async (file: File) => {
    setUploading(true);
    try {
      const fd = new FormData();
      fd.append("file", file);
      await apiClient.post(`/admin/migrations/${jobId}/tarball`, fd, {
        headers: { "Content-Type": "multipart/form-data" },
      });
      message.success("Tarball uploaded successfully");
      onDone();
    } catch (err: unknown) {
      const detail = (err as { response?: { data?: { detail?: string; error?: string } } })
        ?.response?.data?.detail;
      message.error(detail ?? "Upload failed");
    } finally {
      setUploading(false);
    }
  };

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Alert
        type="info"
        showIcon
        message="Upload the WHM pkgacct archive"
        description="Select the cpmove-<user>.tar.gz file produced by WHM's Full Backup / pkgacct tool. Files up to 20 GB are streamed directly — no intermediate buffering."
      />
      <Upload
        accept=".gz,.tar.gz"
        maxCount={1}
        showUploadList={false}
        beforeUpload={(file) => {
          void doUpload(file as unknown as File);
          return false;
        }}
      >
        <Button icon={<UploadOutlined />} loading={uploading}>
          {uploading ? "Uploading…" : "Select cpmove tarball (.tar.gz)"}
        </Button>
      </Upload>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        File name should match the pattern <Typography.Text code style={{ fontSize: 12 }}>cpmove-&lt;user&gt;.tar.gz</Typography.Text>
      </Typography.Text>
    </Space>
  );
}

function ImportStep({ jobId, onDone }: { jobId: string; onDone: () => void }) {
  const [form] = Form.useForm<ImportInput>();

  const mut = useMutation({
    mutationFn: async (vals: ImportInput) => {
      await apiClient.post(`/admin/migrations/${jobId}/import`, vals);
    },
    onSuccess: () => {
      message.success("Import started — running in the background");
      onDone();
    },
    onError: (err: unknown) => {
      const detail = (err as { response?: { data?: { detail?: string } } })
        ?.response?.data?.detail;
      message.error(detail ?? "Failed to start import");
    },
  });

  return (
    <Form form={form} layout="vertical" onFinish={(v) => void mut.mutateAsync(v)}>
      <Alert
        type="info"
        showIcon
        message="Set destination account"
        description="Specify the local username to import into. If the user doesn't exist yet, add email + password to auto-create them. Target package ID is optional — the default package is used when blank."
        style={{ marginBottom: 16 }}
      />
      <Form.Item
        name="target_user"
        label="Target username"
        rules={[{ required: true, message: "Username is required" }]}
        tooltip="The local Jabali username the account will be imported into."
      >
        <Input placeholder="alice" />
      </Form.Item>
      <Form.Item name="target_email" label="Email (for new-user auto-creation)">
        <Input placeholder="alice@example.com" />
      </Form.Item>
      <Form.Item name="target_password" label="Password (for new-user auto-creation)">
        <Input.Password placeholder="leave blank to skip auto-create" autoComplete="off" />
      </Form.Item>
      <Form.Item name="target_package_id" label="Package ID (optional)">
        <Input placeholder="leave blank for default package" />
      </Form.Item>
      <Button type="primary" htmlType="submit" loading={mut.isPending}>
        Start import
      </Button>
    </Form>
  );
}

// ─── steps config ─────────────────────────────────────────────────────────────

const CPANEL_STEPS: { title: string; step: CPanelStep | "create" }[] = [
  { title: "Create job", step: "create" },
  { title: "SSH credentials", step: "secrets" },
  { title: "Pull source", step: "pull" },
  { title: "Run import", step: "import" },
];

const WHM_STEPS: { title: string; step: WHMStep | "create" }[] = [
  { title: "Create job", step: "create" },
  { title: "Upload tarball", step: "tarball" },
  { title: "Run import", step: "import" },
];

function WordPressImportStep({ jobId, onDone }: { jobId: string; onDone: () => void }) {
  const [form] = Form.useForm<{ dest_user: string; dest_domain: string }>();
  const mut = useMutation({
    mutationFn: async (vals: { dest_user: string; dest_domain: string }) => {
      await apiClient.post(`/admin/migrations/${jobId}/import-wp`, vals);
    },
    onSuccess: () => {
      message.success("Import started");
      onDone();
    },
    onError: (err: unknown) => {
      const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
      message.error(detail ?? "Import failed to start");
    },
  });
  return (
    <Form form={form} layout="vertical" onFinish={(v) => void mut.mutateAsync(v)}>
      <Alert
        type="info"
        showIcon
        message="Import the staged WordPress site into a destination"
        description="Provisions a Jabali DB, restores the dump, moves files into the docroot, rewrites wp-config.php, and (on a domain change) runs a serialized-safe search-replace."
        style={{ marginBottom: 16 }}
      />
      <Form.Item name="dest_user" label="Destination OS user" rules={[{ required: true, message: "Dest user required" }]}>
        <Input placeholder="shukivaknin" />
      </Form.Item>
      <Form.Item
        name="dest_domain"
        label="Destination domain"
        tooltip="Docroot = /home/<user>/domains/<domain>/public_html"
        rules={[{ required: true, message: "Dest domain required" }]}
      >
        <Input placeholder="example.com" />
      </Form.Item>
      <Button type="primary" htmlType="submit" loading={mut.isPending}>
        Start import
      </Button>
    </Form>
  );
}

function stepIndex(kind: string, current: DriveStep | "create"): number {
  const steps = kind === "whm_pkgacct" ? WHM_STEPS : CPANEL_STEPS;
  const idx = steps.findIndex((s) => s.step === current);
  return idx >= 0 ? idx : 0;
}

function stepsItems(kind: string, current: DriveStep | "create") {
  const steps = kind === "whm_pkgacct" ? WHM_STEPS : CPANEL_STEPS;
  const cur = stepIndex(kind, current);
  return steps.map((s, i) => ({
    title: s.title,
    status: (i < cur ? "finish" : i === cur ? "process" : "wait") as
      | "finish"
      | "process"
      | "wait",
  }));
}

// ─── main drawer ───────────────────────────────────────────────────────────────

export interface CreateMigrationDrawerProps {
  open: boolean;
  onClose: () => void;
}

export const CreateMigrationDrawer = ({
  open,
  onClose,
}: CreateMigrationDrawerProps) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<CreateInput>();
  const qc = useQueryClient();
  const [created, setCreated] = useState<MigrationJob | null>(null);
  const [driveStep, setDriveStep] = useState<DriveStep>("secrets");

  const create = useMutation<MigrationJob, unknown, CreateInput>({
    mutationFn: async (input) => {
      const { data } = await apiClient.post<MigrationJob>(
        "/admin/migrations",
        input,
      );
      return data;
    },
    onSuccess: async (job) => {
      message.success("Migration job created");
      setCreated(job);
      setDriveStep(job.source_kind === "whm_pkgacct" ? "tarball" : "secrets");
      await qc.invalidateQueries({ queryKey: ["admin-migrations"] });
    },
    onError: (err) => {
      const detail =
        (err as { response?: { data?: { error?: string; detail?: string } } })
          ?.response?.data?.detail;
      message.error(detail ?? "Create failed");
    },
  });

  const handleSubmit = async (values: CreateInput) => {
    if (values.source_kind === "wordpress_plugin" && !values.source_user) {
      values = { ...values, source_user: "wp" }; // token-authed; placeholder for the unique key
    }
    await create.mutateAsync(values);
  };

  const handleDone = () => {
    form.resetFields();
    setCreated(null);
    setDriveStep("secrets");
    onClose();
  };

  // GH #327: every source kind (cPanel, WHM, DirectAdmin, HestiaCP, WordPress
  // SSH/plugin) is driven end-to-end by the standard secrets → pull → import
  // flow — there is no scaffold-only "contact support" path any more.

  return (
    <Drawer
      title={t("createmigrationdrawer.new_migration_job")}
      open={open}
      onClose={handleDone}
      width={640}
      destroyOnClose
    >
      {!created ? (
        // ── Step 1: create form ──────────────────────────────────────────────
        <Form<CreateInput>
          form={form}
          layout="vertical"
          onFinish={handleSubmit}
          initialValues={{ source_kind: "cpanel", source_port: 22 }}
        >
          <Form.Item
            label={t("createmigrationdrawer.source_kind")}
            name="source_kind"
            rules={[{ required: true, message: "Pick a source panel kind" }]}
          >
            <SourceCards />
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(a, b) => a.source_kind !== b.source_kind}>
            {({ getFieldValue }) => {
              const k = getFieldValue("source_kind");
              const isPlugin = k === "wordpress_plugin";
              return (
                <Form.Item
                  label={isPlugin ? "Source site URL" : "Source host"}
                  name="source_host"
                  tooltip={isPlugin ? "The source WordPress site URL (https://old-site.com)." : "Hostname or IP of the source panel. Prefer the direct IP if the server sits behind Cloudflare/a proxy — a hostname can resolve to the proxy instead of the source. Not required for WHM tarball uploads."}
                >
                  <Input placeholder={isPlugin ? "https://old-site.com" : "203.0.113.10 or src.example.com"} />
                </Form.Item>
              );
            }}
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(a, b) => a.source_kind !== b.source_kind}>
            {({ getFieldValue }) => {
              const k = getFieldValue("source_kind");
              if (k === "wordpress_plugin") {
                // token-authed — no SSH user. (source_user is auto-set to "wp"
                // on submit to satisfy the unique key.)
                return null;
              }
              return (
                <Form.Item
                  label={k === "wordpress_ssh" ? "SSH user" : "Source user"}
                  name="source_user"
                  rules={[{ required: true, message: "SSH username required" }]}
                  tooltip={k === "wordpress_ssh" ? "SSH login on the source. Cloudways: the master_xxx user (needs an SSH KEY, not a password)." : "Login name on the source panel."}
                >
                  <Input placeholder={k === "wordpress_ssh" ? "root or master_xxxx" : "bob"} />
                </Form.Item>
              );
            }}
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(a, b) => a.source_kind !== b.source_kind}>
            {({ getFieldValue }) => {
              const k = getFieldValue("source_kind");
              // No SSH for token-plugin or offline tarball sources.
              if (k === "wordpress_plugin" || k === "whm_pkgacct") {
                return null;
              }
              return (
                <Form.Item
                  label={t("createmigrationdrawer.ssh_port")}
                  name="source_port"
                  tooltip={t("createmigrationdrawer.the_source_server_s_ssh_port_defaults_to_22")}
                >
                  <InputNumber min={1} max={65535} style={{ width: 140 }} />
                </Form.Item>
              );
            }}
          </Form.Item>
          <Form.Item noStyle shouldUpdate>
            {({ getFieldValue }) =>
              getFieldValue("source_kind") === "wordpress_ssh" ? (
                <Form.Item
                  label={t("createmigrationdrawer.wordpress_path_optional")}
                  name="source_path"
                  tooltip={t("createmigrationdrawer.absolute_path_to_the_wordpress_root_on_the_s")}
                >
                  <Input placeholder="/home/master/applications/xxxx/public_html" />
                </Form.Item>
              ) : null
            }
          </Form.Item>

          <Space>
            <Button
              type="primary"
              htmlType="submit"
              loading={create.isPending}
            >
              Create job
            </Button>
            <Button onClick={handleDone}>Cancel</Button>
          </Space>
        </Form>
      ) : (
        // ── Drive wizard: secrets → pull → import (cPanel)
        //                  tarball → import (WHM) ────────────────────────────
        <Space direction="vertical" size="large" style={{ width: "100%" }}>
          <Alert
            type="success"
            showIcon
            message={`Job created: ${created.id}`}
            description={`Source: ${created.source_host || "—"}  ·  User: ${created.source_user}`}
          />

          {driveStep !== "done" && (
            <Steps
              size="small"
              current={stepIndex(created.source_kind, driveStep)}
              items={stepsItems(created.source_kind, driveStep)}
            />
          )}

          {driveStep === "secrets" && (
            <SecretsStep jobId={created.id} kind={created.source_kind} onDone={() => setDriveStep("pull")} />
          )}

          {driveStep === "pull" && (
            <PullStep jobId={created.id} onDone={() => setDriveStep("import")} />
          )}

          {driveStep === "tarball" && (
            <TarballStep jobId={created.id} onDone={() => setDriveStep("import")} />
          )}

          {driveStep === "import" &&
            (created.source_kind === "wordpress_ssh" || created.source_kind === "wordpress_plugin" ? (
              <WordPressImportStep jobId={created.id} onDone={() => setDriveStep("done")} />
            ) : (
              <ImportStep jobId={created.id} onDone={() => setDriveStep("done")} />
            ))}

          {driveStep === "done" && (
            <Space direction="vertical" size="middle" style={{ width: "100%" }}>
              <Alert
                type="success"
                showIcon
                message={t("createmigrationdrawer.import_started")}
                description={t("createmigrationdrawer.the_migration_is_running_in_the_background_o")}
              />
              <Button type="primary" onClick={handleDone}>
                Done
              </Button>
            </Space>
          )}
        </Space>
      )}
    </Drawer>
  );
};
