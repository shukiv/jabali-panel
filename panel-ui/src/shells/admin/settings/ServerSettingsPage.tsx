import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { useTabParam } from "../../../hooks/useTabParam";
import {
  BgColorsOutlined,
  CheckOutlined,
  CodeOutlined,
  CloseOutlined,
  DatabaseOutlined,
  FileTextOutlined,
  GlobalOutlined,
  HddOutlined,
  MailOutlined,
  SaveOutlined,
  SettingOutlined,
  WarningOutlined,
  AppstoreOutlined,
  ToolOutlined,
} from "@icons";
import {
  Alert,
  Button,
  Card,
  Col,
  Divider,
  Form,
  Input,
  InputNumber,
  Modal,
  Row,
  Grid,
  Select,
  Space,
  Switch,
  Tabs,
  Typography,
  notification,
} from "antd";

// Post-M21 notify shim: matches the Refine useNotification().open
// contract (`{ type, message, description }`) so callers don't have
// to change. Forwards to AntD's native `notification.open`.
type NotifyInput = {
  type?: "success" | "error" | "warning" | "info";
  message: string;
  description?: React.ReactNode;
};
function useNotify() {
  return (input: NotifyInput) => {
    notification.open({
      message: input.message,
      description: input.description,
      type: input.type,
    });
  };
}

import { apiClient } from "../../../apiClient";
import { BrandingCard } from "./BrandingCard";
import { LookAndFeelCard } from "./LookAndFeelCard";
import { DatabasesCard } from "./DatabasesCard";
import { DockerMarketplaceCard } from "./DockerMarketplaceCard";
import { PythonAppsCard } from "./PythonAppsCard";
import { PHPPerformanceModesCard } from "./PHPPerformanceModesCard";
import { DatabaseAdminSections } from "./DatabaseAdminSections";
import { DNSResolversCard } from "./DNSResolversCard";
import { DNSPermissionsCard } from "./DNSPermissionsCard";
import { EmailCard } from "./EmailCard";
import { FreeHostnameCard } from "./FreeHostnameCard";
import { WebmailToggleCard } from "./WebmailToggleCard";
import { Dkim2ToggleCard } from "./Dkim2ToggleCard";
import { ModulesCard } from "./ModulesCard";
import { StalwartWebadminCard } from "./StalwartWebadminCard";
import { PageTemplatesCard } from "./PageTemplatesCard";
import { AccountSkeletonCard } from "./AccountSkeletonCard";
import { PanelSSLCard } from "./PanelSSLCard";
import { NspawnImagesCard } from "./NspawnImagesCard";
import { SSOMaintenanceCard } from "./SSOMaintenanceCard";
import { TenantDomainOptionsCard } from "./TenantDomainOptionsCard";
import { TenantNotificationsCard } from "./TenantNotificationsCard";
import { NginxSettingsCard } from "./NginxSettingsCard";
import { LogRetentionCard } from "./LogRetentionCard";

type ServerSettings = {
  id: number;
  hostname: string;
  preview_base?: string;
  public_ipv4: string;
  public_ipv6: string;
  ns1_name: string;
  ns1_ipv4: string;
  ns2_name: string;
  ns2_ipv4: string;
  admin_email: string;
  default_dns_ttl: number;
  timezone: string;
  ssh_port: number;
  ssh_password_auth: boolean;
  ssh_user_password_auth: boolean;
  ssh_sandbox_mode: "bubblewrap" | "nspawn";
  default_nspawn_image_version: string;
  root_terminal_enabled: boolean;
  disk_quota_enabled: boolean;
  bandwidth_quota_enforce_enabled: boolean;
  upload_max_size_mb: number;
  postgres_enabled: boolean;
  postgres_max_connections_per_user: number;
  migration_allow_private_hosts: boolean;
  working_folder: string;
  updated_at: string;
};

type NspawnImage = {
  name: string;
  manifest?: string;
};

// notifyError narrows axios-style errors to a friendly message.
type NotifyFn = ReturnType<typeof useNotify>;
function notifyError(notify: NotifyFn, title: string, err: unknown) {
  const e = err as { response?: { data?: { detail?: string } }; message?: string };
  notify({
    type: "error",
    message: title,
    description: e.response?.data?.detail ?? e.message ?? "Unknown error",
  });
}

// GeneralSettingsTab — Identity + Server Time + Root Terminal. Owns its own
// form + Save button; PATCH /admin/settings supports partial updates so
// sending only this tab's fields doesn't disturb DNS settings.
const GeneralSettingsTab = () => {
  const { t } = useTranslation();
  const [form] = Form.useForm<ServerSettings>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [originalHostname, setOriginalHostname] = useState("");
  const notify = useNotify();

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<ServerSettings>("/admin/settings");
        if (cancelled) return;
        form.setFieldsValue(resp.data);
        setOriginalHostname(resp.data.hostname);
      } catch (err) {
        notifyError(notify, "Failed to load settings", err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSubmit = async (values: ServerSettings) => {
    setSaving(true);
    try {
      const resp = await apiClient.patch<ServerSettings>("/admin/settings", {
        hostname: values.hostname,
        preview_base: values.preview_base || "",
        public_ipv4: values.public_ipv4,
        public_ipv6: values.public_ipv6 || "",
        admin_email: values.admin_email || "",
        timezone: values.timezone || "",
        // Root Terminal toggle lives on General (next to SSH/identity)
        // rather than Storage — moved 2026-06-04 because operators kept
        // missing it under the Storage tab where it logically didn't
        // fit. Storage tab no longer PATCHes this field.
        root_terminal_enabled: values.root_terminal_enabled || false,
      });
      notify({ type: "success", message: "Settings saved" });
      form.setFieldsValue(resp.data);
      setOriginalHostname(resp.data.hostname);
    } catch (err) {
      notifyError(notify, "Failed to save", err);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Form
      form={form}
      layout="vertical"
      onFinish={handleSubmit}
      disabled={loading}
    >
      <Form.Item
        shouldUpdate={(prev, cur) => prev.hostname !== cur.hostname}
        noStyle
      >
        {({ getFieldValue }) => {
          const current = getFieldValue("hostname");
          if (!originalHostname || current === originalHostname) return null;
          return (
            <Alert
              type="warning"
              showIcon
              icon={<WarningOutlined />}
              title={t("settings.hostname_change")}
              description={
                <>
                  Changing the hostname updates the OS hostname and the
                  default nameserver names. <b>Any existing registrar NS
                  delegations using the old hostname will break</b> — all
                  hosted domain owners must update their registrar records
                  to point at <code>ns1.{current}</code> /{" "}
                  <code>ns2.{current}</code>.
                </>
              }
              style={{ marginBottom: 16 }}
            />
          );
        }}
      </Form.Item>

      <Card title={t("settings.identity")} style={{ marginBottom: 16 }}>
        <Row gutter={16}>
          <Col xs={24} md={12}>
            <Form.Item
              label={t("settings.hostname")}
              name="hostname"
              rules={[
                { required: true, message: "Hostname required" },
                {
                  pattern:
                    /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/,
                  message: "Invalid hostname",
                },
              ]}
              extra="Fully-qualified name for this server (e.g. panel.example.com)."
            >
              <Input placeholder="panel.example.com" />
            </Form.Item>
            <Form.Item
              label="Preview URL base"
              name="preview_base"
              rules={[
                {
                  pattern:
                    /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/,
                  message: "Invalid hostname",
                },
              ]}
              extra={
                <>
                  Domain previews serve at &lt;site&gt;.&lt;base&gt;. Empty ={" "}
                  <code>preview.&lt;hostname&gt;</code>. If this server's
                  hostname doesn't resolve publicly, set a custom domain you
                  control (e.g. <code>preview.example.com</code>, wildcard
                  pointed here) or a magic-DNS base like{" "}
                  <code>203-0-113-7.sslip.io</code> (your IP with dashes —
                  resolves without any DNS setup; previews serve over HTTP).
                </>
              }
            >
              <Input placeholder="preview.example.com (optional)" allowClear />
            </Form.Item>
          </Col>
          <Col xs={24} md={12}>
            <Form.Item
              label={t("settings.admin_email")}
              name="admin_email"
              rules={[{ type: "email", message: "Invalid email" }]}
              extra="Used as the registration email for Let's Encrypt / ACME. Required before issuing SSL certificates."
            >
              <Input placeholder="admin@example.com" />
            </Form.Item>
          </Col>
        </Row>

        <Row gutter={16}>
          <Col xs={24} md={12}>
            <Form.Item
              label={t("settings.public_ipv4")}
              name="public_ipv4"
              rules={[
                { required: true, message: "IPv4 required" },
                {
                  pattern: /^[0-9]{1,3}(\.[0-9]{1,3}){3}$/,
                  message: "Invalid IPv4",
                },
              ]}
            >
              <Input placeholder="203.0.113.5" />
            </Form.Item>
          </Col>
          <Col xs={24} md={12}>
            <Form.Item
              label={t("settings.public_ipv6_optional")}
              name="public_ipv6"
              rules={[
                {
                  pattern: /^$|^[0-9a-fA-F:]+$/,
                  message: "Invalid IPv6",
                },
              ]}
            >
              <Input placeholder="2001:db8::1" />
            </Form.Item>
          </Col>
        </Row>
      </Card>

      <Card title={t("settings.server_time")} style={{ marginBottom: 16 }}>
        <Form.Item
          label={t("settings.timezone")}
          name="timezone"
          rules={[{ required: false }]}
          extra="Select your server's timezone. Changes take effect immediately."
        >
          <Select
            placeholder={t("settings.select_timezone")}
            allowClear
            showSearch
            optionFilterProp="children"
            filterOption={(input, option) =>
              (option?.label ?? "").toLowerCase().includes(input.toLowerCase())
            }
            options={Array.from(Intl.supportedValuesOf("timeZone")).map((tz) => ({
              label: tz,
              value: tz,
            }))}
          />
        </Form.Item>
      </Card>

      <SSOMaintenanceCard />
      <PanelSSLCard />
      <Card title={t("settings.root_terminal_m45")} style={{ marginBottom: 16 }}>
        <Row gutter={16}>
          <Col xs={24}>
            <div style={{ marginBottom: 16 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
                <Form.Item name="root_terminal_enabled" valuePropName="checked" noStyle>
                  <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
                </Form.Item>
                <Typography.Text>Enable in-panel root shell</Typography.Text>
              </div>
              <Typography.Text type="secondary">
                Exposes a true unrestricted root terminal (uid 0) in the admin panel.
                Off by default. One-shot IP+admin-bound token; every byte of every
                session is recorded to /var/log/jabali/terminal/&lt;id&gt;.cast and a
                critical notification is sent on open. Only enable if you accept an
                authenticated-admin RCE surface.
              </Typography.Text>
              <Alert
                style={{ marginTop: 12 }}
                type="warning"
                showIcon
                message={t("settings.this_is_the_highest_risk_feature_in_the_panel_le")}
              />
            </div>
          </Col>
        </Row>
      </Card>

      <ModulesCard />

      <Space>
        <Button
          type="primary"
          icon={<SaveOutlined />}
          loading={saving}
          onClick={() => form.submit()}
        >
          Save Settings
        </Button>
      </Space>
    </Form>
  );
};

// DNSSettingsTab — DNS Nameservers card. Independent form + Save button;
// PATCH /admin/settings only writes the ns* fields it sends so this
// doesn't clobber identity/SSH/timezone settings.
const DNSSettingsTab = () => {
  const { t } = useTranslation();
  const [form] = Form.useForm<ServerSettings>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const notify = useNotify();

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<ServerSettings>("/admin/settings");
        if (cancelled) return;
        form.setFieldsValue(resp.data);
      } catch (err) {
        notifyError(notify, "Failed to load settings", err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSubmit = async (values: ServerSettings) => {
    setSaving(true);
    try {
      const resp = await apiClient.patch<ServerSettings>("/admin/settings", {
        ns1_name: values.ns1_name || "",
        ns1_ipv4: values.ns1_ipv4 || "",
        ns2_name: values.ns2_name || "",
        ns2_ipv4: values.ns2_ipv4 || "",
      });
      notify({ type: "success", message: "DNS nameservers saved" });
      form.setFieldsValue(resp.data);
    } catch (err) {
      notifyError(notify, "Failed to save", err);
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <DNSResolversCard />
      <DNSPermissionsCard />

      <Form
        form={form}
        layout="vertical"
        onFinish={handleSubmit}
        disabled={loading}
      >
        <Card title={t("settings.dns_nameservers")} style={{ marginBottom: 16 }}>
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          These are the names and addresses your customers will set at their
          registrar. You typically run ns1 on this server and ns2 on a
          separate box. ns2 is optional at first — fill it in once you have
          a second nameserver provisioned.
        </Typography.Paragraph>

        <Row gutter={16}>
          <Col xs={24} md={12}>
            <Form.Item label="ns1 hostname" name="ns1_name">
              <Input placeholder="ns1.panel.example.com" />
            </Form.Item>
          </Col>
          <Col xs={24} md={12}>
            <Form.Item label="ns1 IPv4" name="ns1_ipv4">
              <Input placeholder="203.0.113.5" />
            </Form.Item>
          </Col>
        </Row>

        <Divider titlePlacement="left" plain>
          Secondary (optional)
        </Divider>

        <Row gutter={16}>
          <Col xs={24} md={12}>
            <Form.Item label="ns2 hostname" name="ns2_name">
              <Input placeholder="ns2.panel.example.com" />
            </Form.Item>
          </Col>
          <Col xs={24} md={12}>
            <Form.Item label="ns2 IPv4" name="ns2_ipv4">
              <Input placeholder="" />
            </Form.Item>
          </Col>
        </Row>
      </Card>

      <Card title={t("settings.dns_record_defaults")} style={{ marginBottom: 16 }}>
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          Default TTL (in seconds) applied to newly-created DNS records
          when the API caller does not specify one. Range 60–86400 (1
          minute to 1 day). The hardcoded fallback before this setting
          was 3600 (1 hour).
        </Typography.Paragraph>
        <Row gutter={16}>
          <Col xs={24} md={12}>
            <Form.Item
              label={t("settings.default_record_ttl_seconds")}
              name="default_dns_ttl"
              rules={[
                { required: true, message: "Required" },
                { type: "number", min: 60, max: 86400, message: "Must be 60–86400" },
              ]}
            >
              <InputNumber
                min={60}
                max={86400}
                step={60}
                style={{ width: "100%" }}
                placeholder="3600"
              />
            </Form.Item>
          </Col>
        </Row>
      </Card>

        <Space>
          <Button
            type="primary"
            icon={<SaveOutlined />}
            loading={saving}
            htmlType="submit"
          >
            Save DNS Settings
          </Button>
        </Space>
      </Form>
    </>
  );
};

// StorageSettingsTab — File Manager upload cap + POSIX quota enforcement.
// Owns its own form so unsaved edits are independent of the General tab,
// and the partial PATCH only ships the two storage fields so a Save here
// can't clobber identity/SSH/timezone settings.
const StorageSettingsTab = () => {
  const { t } = useTranslation();
  const [form] = Form.useForm<ServerSettings>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const notify = useNotify();

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<ServerSettings>("/admin/settings");
        if (cancelled) return;
        form.setFieldsValue(resp.data);
      } catch (err) {
        notifyError(notify, "Failed to load settings", err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSubmit = async (values: ServerSettings) => {
    setSaving(true);
    try {
      const resp = await apiClient.patch<ServerSettings>("/admin/settings", {
        disk_quota_enabled: values.disk_quota_enabled || false,
        bandwidth_quota_enforce_enabled: values.bandwidth_quota_enforce_enabled || false,
        upload_max_size_mb: values.upload_max_size_mb || 1024,
        working_folder: (values.working_folder || "").trim() || "/var/lib/jabali",
      });
      notify({ type: "success", message: "Storage settings saved" });
      form.setFieldsValue(resp.data);
    } catch (err) {
      notifyError(notify, "Failed to save", err);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Form
      form={form}
      layout="vertical"
      onFinish={handleSubmit}
      disabled={loading}
    >
      <Card title={t("settings.file_manager")} style={{ marginBottom: 16 }}>
        <Row gutter={16}>
          <Col xs={24} md={12}>
            <Form.Item
              label={t("settings.maximum_upload_size_mb")}
              name="upload_max_size_mb"
              rules={[
                { required: true, message: "Required" },
                {
                  type: "number",
                  min: 1,
                  max: 10240,
                  message: "Between 1 and 10240 MB",
                },
              ]}
              tooltip="Hard cap on a single file upload via the File Manager. Applies to both single-multipart and chunked paths. Defaults to 1024 MB (1 GB)."
            >
              <InputNumber
                min={1}
                max={10240}
                step={64}
                style={{ width: "100%" }}
                addonAfter="MB"
              />
            </Form.Item>
          </Col>
        </Row>
      </Card>

      <Card title={t("settings.working_folder")} style={{ marginBottom: 16 }}>
        <Row gutter={16}>
          <Col xs={24}>
            <Form.Item
              label={t("settings.working_folder")}
              name="working_folder"
              rules={[
                { required: true, message: "Required" },
                {
                  validator: async (_, v: string) => {
                    if (!v) return;
                    if (!v.startsWith("/")) throw new Error("Must be an absolute path");
                    if (v.includes("..")) throw new Error("Must not contain '..'");
                  },
                },
              ]}
              tooltip="Base directory for migration staging trees + backup repositories. Default /var/lib/jabali. Subdirs created on first use: <working_folder>/migrations and <working_folder>/backups. Retarget to a larger disk by changing this + symlinking the legacy /var/lib/jabali-migrations + /var/lib/jabali-backups paths underneath."
            >
              <Input placeholder="/var/lib/jabali" />
            </Form.Item>
            <Alert
              type="info"
              showIcon
              message={t("settings.disk_size_matters")}
              description={
                <>
                  Migrations can stage 5+ GB per account; backups grow without bound until
                  retention prunes. Point at a dedicated data volume in production
                  (e.g. <code>/mnt/storage/jabali</code>) so a runaway migration doesn't fill
                  the root partition.
                </>
              }
            />
          </Col>
        </Row>
      </Card>

      <Card title={t("settings.disk_quotas")} style={{ marginBottom: 16 }}>
        <Row gutter={16}>
          <Col xs={24}>
            <div style={{ marginBottom: 16 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
                <Form.Item name="disk_quota_enabled" valuePropName="checked" noStyle>
                  <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
                </Form.Item>
                <Typography.Text>POSIX Disk Quota Enforcement</Typography.Text>
              </div>
              <Typography.Text type="secondary">
                When enabled, the reconciler applies per-user disk-quota limits from packages and overrides.
                When disabled, disk-quota fields in Packages are read-only and only cgroup limits (cpu / memory / io / tasks) are enforced.
              </Typography.Text>
              <Alert
                style={{ marginTop: 12 }}
                type="info"
                showIcon
                message={t("settings.kernel_posix_quota_must_be_active_on_the_filesys")}
                description={
                  <>
                    install.sh wires this up automatically (works on dedicated <code>/home</code> partitions
                    and on <code>/</code>-shared <code>/home</code> via ext4 hidden quota inodes).
                    Only system UIDs ≥ 1000 ever get a setquota call, so root + system daemons stay
                    unlimited. Verify with <code>quotaon -p -a</code> before flipping this on; if no
                    quota is reported active, re-run install.sh or set it up manually.
                  </>
                }
              />
            </div>

            <div style={{ marginTop: 24, marginBottom: 16 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
                <Form.Item name="bandwidth_quota_enforce_enabled" valuePropName="checked" noStyle>
                  <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
                </Form.Item>
                <Typography.Text>Bandwidth Quota Auto-Suspend (M13.1.1)</Typography.Text>
              </div>
              <Typography.Text type="secondary">
                When enabled, the reconciler disables every owned domain of a user whose monthly
                bandwidth ≥ <code>BandwidthQuotaMB</code> (package limit). Domains auto-restore once
                usage drops back below 80%. Only panel-driven suspensions are auto-restored — manual
                admin disables stay disabled.
              </Typography.Text>
              <Alert
                style={{ marginTop: 12 }}
                type="warning"
                showIcon
                message={t("settings.off_by_default_opt_in_feature")}
                description={
                  <>
                    Bandwidth data flows from the M13.1 daily goaccess scan; on a fresh install
                    the table is empty until the first 24 h cycle. The notification path
                    (<code>bandwidth.quota.warn</code> / <code>.crit</code>) fires regardless
                    of this toggle — the toggle ONLY controls whether the reconciler also
                    flips <code>is_enabled=false</code> on the user's domains.
                  </>
                }
              />
            </div>
          </Col>
        </Row>
      </Card>

      <Space>
        <Button
          type="primary"
          icon={<SaveOutlined />}
          loading={saving}
          htmlType="submit"
        >
          Save Storage Settings
        </Button>
      </Space>
    </Form>
  );
};

type SettingsTabKey =
  | "general"
  | "ssh"
  | "storage"
  | "dns"
  | "email"
  | "databases"
  | "apps"
  | "nginx"
  | "branding"
  | "logs";

const BrandingSettingsTab = () => (
  <>
    <BrandingCard />
      <LookAndFeelCard />
    <PageTemplatesCard />
    <AccountSkeletonCard />
  </>
);

// SSHSettingsTab — SSH Access (port + password auth) and the Shell Sandbox
// (bubblewrap/nspawn mode + default nspawn image) plus nspawn image management.
// Own form + Save; PATCH /admin/settings is partial so this tab sends only its
// SSH-related fields and never disturbs General/DNS/etc.
const SSHSettingsTab = () => {
  const { t } = useTranslation();
  const [form] = Form.useForm<ServerSettings>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [originalSSHPort, setOriginalSSHPort] = useState(22);
  const [originalSSHPasswordAuth, setOriginalSSHPasswordAuth] = useState(false);
  const [originalSSHUserPasswordAuth, setOriginalSSHUserPasswordAuth] = useState(false);
  const [nspawnImages, setNspawnImages] = useState<NspawnImage[]>([]);
  const notify = useNotify();

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<ServerSettings>("/admin/settings");
        if (cancelled) return;
        form.setFieldsValue(resp.data);
        setOriginalSSHPort(resp.data.ssh_port || 22);
        setOriginalSSHPasswordAuth(resp.data.ssh_password_auth || false);
        setOriginalSSHUserPasswordAuth(resp.data.ssh_user_password_auth || false);
        try {
          const imgResp = await apiClient.get<{ images: NspawnImage[] }>(
            "/system/nspawn-images",
          );
          if (!cancelled) setNspawnImages(imgResp.data.images || []);
        } catch {
          // Empty list is fine — admin sees a placeholder + warning.
        }
      } catch (err) {
        notifyError(notify, "Failed to load settings", err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSubmit = async (values: ServerSettings) => {
    setSaving(true);
    try {
      const resp = await apiClient.patch<ServerSettings>("/admin/settings", {
        ssh_port: values.ssh_port || 22,
        ssh_password_auth: values.ssh_password_auth || false,
        ssh_user_password_auth: values.ssh_user_password_auth || false,
        ssh_sandbox_mode: values.ssh_sandbox_mode || "bubblewrap",
        ...(values.default_nspawn_image_version
          ? { default_nspawn_image_version: values.default_nspawn_image_version }
          : {}),
      });
      notify({ type: "success", message: "Settings saved" });
      form.setFieldsValue(resp.data);
      setOriginalSSHPort(resp.data.ssh_port || 22);
      setOriginalSSHPasswordAuth(resp.data.ssh_password_auth || false);
      setOriginalSSHUserPasswordAuth(resp.data.ssh_user_password_auth || false);
    } catch (err) {
      notifyError(notify, "Failed to save", err);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Form
      form={form}
      layout="vertical"
      onFinish={handleSubmit}
      disabled={loading}
    >
      <Card title={t("settings.ssh_access")} style={{ marginBottom: 16 }}>
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          Configure SSH port and authentication method. Changes are applied
          immediately and are reversible.
        </Typography.Paragraph>

        <Row gutter={16}>
          <Col span={24}>
            <Form.Item
              label={t("settings.ssh_port")}
              name="ssh_port"
              rules={[
                { required: true, message: "SSH port required" },
                {
                  type: "number",
                  min: 1,
                  max: 65535,
                  message: "Port must be between 1 and 65535",
                },
              ]}
              extra="Standard SSH port is 22. Change to reduce automated attack attempts."
            >
              <InputNumber min={1} max={65535} style={{ width: 200 }} />
            </Form.Item>
          </Col>
        </Row>
        <Row gutter={16}>
          <Col xs={24} md={12}>
            <div style={{ marginBottom: 24 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
                <Form.Item name="ssh_password_auth" valuePropName="checked" noStyle>
                  <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
                </Form.Item>
                <Typography.Text>Root Password Authentication</Typography.Text>
              </div>
              <Typography.Text type="secondary">
                Allow root and other non-hosting users to log in with a password. Key-based authentication is always available.
              </Typography.Text>
            </div>
          </Col>
          <Col xs={24} md={12}>
            <div style={{ marginBottom: 24 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
                <Form.Item name="ssh_user_password_auth" valuePropName="checked" noStyle>
                  <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
                </Form.Item>
                <Typography.Text>User Password Authentication</Typography.Text>
              </div>
              <Typography.Text type="secondary">
                Allow hosting users (jabali-sftp group) to authenticate with a password. They are still SFTP-only — no shell.
              </Typography.Text>
            </div>
          </Col>
        </Row>

        <Divider style={{ margin: "8px 0 16px" }}>Shell Sandbox</Divider>

        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          SSH-shell users land in a sandbox. Bubblewrap is lightweight
          and runs against the host kernel; nspawn boots an ephemeral
          systemd-nspawn container off a sealed, versioned rootfs.
          <b> Mode change applies on the next SSH connect — no reload needed.</b>
        </Typography.Paragraph>

        <Row gutter={16}>
          <Col xs={24} md={12}>
            <Form.Item
              label={t("settings.sandbox_mode")}
              name="ssh_sandbox_mode"
              rules={[{ required: true }]}
              extra="Bubblewrap = no rootfs needed. nspawn = build an image first via 'jabali nspawn build'."
            >
              <Select
                options={[
                  { value: "bubblewrap", label: "Bubblewrap (default, lightweight)" },
                  { value: "nspawn", label: "systemd-nspawn (full container)" },
                ]}
                style={{ width: "100%" }}
              />
            </Form.Item>
          </Col>
          <Col xs={24} md={12}>
            <Form.Item
              label={t("settings.default_nspawn_image")}
              name="default_nspawn_image_version"
              extra={
                nspawnImages.length === 0
                  ? "No images built yet. Run 'jabali nspawn build --version v1 --snapshot ...' to seed."
                  : "Pinned to new SSH-enabled users at create. Existing users keep their pin."
              }
            >
              <Select
                showSearch
                allowClear
                placeholder="debian-12-v1"
                options={nspawnImages.map((img) => ({
                  value: img.name,
                  label: img.name,
                }))}
                style={{ width: "100%" }}
              />
            </Form.Item>
          </Col>
        </Row>
      </Card>

      <NspawnImagesCard />

      <Space>
        <Button
          type="primary"
          icon={<SaveOutlined />}
          loading={saving}
          onClick={() => {
            const currentSSHPort = form.getFieldValue("ssh_port") || 22;
            const currentSSHPasswordAuth =
              form.getFieldValue("ssh_password_auth") || false;
            const currentSSHUserPasswordAuth =
              form.getFieldValue("ssh_user_password_auth") || false;

            const sshPortChanged = currentSSHPort !== originalSSHPort;
            const sshAuthChanged =
              currentSSHPasswordAuth !== originalSSHPasswordAuth;
            const sshUserAuthChanged =
              currentSSHUserPasswordAuth !== originalSSHUserPasswordAuth;

            if (sshPortChanged || sshAuthChanged || sshUserAuthChanged) {
              Modal.confirm({
                title: "Confirm SSH Configuration Change",
                content: (
                  <Alert
                    type="warning"
                    showIcon
                    title={t("settings.potential_lockout_risk")}
                    description={
                      <>
                        Changing SSH settings may affect your ability to
                        connect remotely. <b>Make sure you have:</b>
                        <ul>
                          <li>Verified the new SSH port or authentication method works</li>
                          <li>An alternative way to access the server if the changes break connectivity</li>
                          <li>The ability to roll back quickly if needed</li>
                        </ul>
                      </>
                    }
                    style={{ marginBottom: 12 }}
                  />
                ),
                okText: "Apply Changes",
                okType: "primary",
                cancelText: "Cancel",
                icon: <WarningOutlined />,
                onOk() {
                  form.submit();
                },
              });
            } else {
              form.submit();
            }
          }}
        >
          Save Settings
        </Button>
      </Space>
    </Form>
  );
};

// tabLabel renders an icon + text label for a settings tab.
const tabLabel = (icon: React.ReactNode, text: string) => (
  <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
    {icon}
    {text}
  </span>
);

export const ServerSettingsPage = () => {
  const [activeTab, setActiveTab] = useTabParam<SettingsTabKey>("general");

  // GH #688 follow-up: below lg the left tab rail ate most of the width, so
  // long-labelled content (Databases especially) overflowed the viewport.
  // Collapse the rail into a Select above the content on small screens —
  // same breakpoint helper AdminLayout uses for its Sider→Drawer swap
  // (ADR-0046).
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg !== false;

  // GH #688: left-positioned card Tabs instead of the old horizontal tab strip
  // that overflowed into a scrollbar. Each tab still owns an independent form;
  // destroyInactiveTabPane unmounts the inactive one so unsaved edits are
  // dropped on switch (mirrors the Users page) and every form isn't mounted at
  // once.
  const items = [
    {
      key: "general",
      label: tabLabel(<SettingOutlined />, "General"),
      children: (
        <>
          <GeneralSettingsTab />
          <FreeHostnameCard />
          <TenantNotificationsCard />
        </>
      ),
    },
    { key: "ssh", label: tabLabel(<CodeOutlined />, "SSH"), children: <SSHSettingsTab /> },
    { key: "storage", label: tabLabel(<HddOutlined />, "Storage"), children: <StorageSettingsTab /> },
    { key: "dns", label: tabLabel(<GlobalOutlined />, "DNS"), children: <DNSSettingsTab /> },
    {
      key: "email",
      label: tabLabel(<MailOutlined />, "Email"),
      children: (
        <>
          <EmailCard />
          <WebmailToggleCard />
          <Dkim2ToggleCard />
          <StalwartWebadminCard />
        </>
      ),
    },
    {
      key: "databases",
      label: tabLabel(<DatabaseOutlined />, "Databases"),
      children: (
        <>
          <DatabasesCard />
          <DatabaseAdminSections />
        </>
      ),
    },
    {
      key: "apps",
      label: tabLabel(<AppstoreOutlined />, "Apps"),
      children: (
        <Space direction="vertical" style={{ width: "100%" }} size="large">
          <DockerMarketplaceCard />
          <PythonAppsCard />
          <PHPPerformanceModesCard />
        </Space>
      ),
    },
    {
      key: "nginx",
      label: tabLabel(<ToolOutlined />, "Nginx"),
      children: (
        <>
          <NginxSettingsCard />
          <TenantDomainOptionsCard />
        </>
      ),
    },
    { key: "branding", label: tabLabel(<BgColorsOutlined />, "Branding"), children: <BrandingSettingsTab /> },
    { key: "logs", label: tabLabel(<FileTextOutlined />, "Logs"), children: <LogRetentionCard /> },
  ];

  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <SettingOutlined /> Server Settings
      </Typography.Title>

      {isDesktop ? (
        /* Tabs sit directly on the page — the left tab bar is NOT wrapped in a
           Card (the individual card-style tabs are the only card chrome). */
        <Tabs
          type="card"
          tabPosition="left"
          activeKey={activeTab}
          onChange={(k) => setActiveTab(k as SettingsTabKey)}
          destroyInactiveTabPane
          items={items}
          // GH #688: keep the left tab bar in view while a long tab's content
          // scrolls. Sticky to the top of the scroll container; a very long
          // tab list scrolls on its own via maxHeight/overflow.
          tabBarStyle={{
            position: "sticky",
            top: 16,
            alignSelf: "flex-start",
            maxHeight: "calc(100vh - 32px)",
            overflowY: "auto",
          }}
        />
      ) : (
        /* GH #688 follow-up: on phones the left rail left too little room and
           wide content (Databases) overflowed the viewport. A full-width
           Select gives the section list back every pixel of width, and only
           the active section is rendered — matching destroyInactiveTabPane
           above, so an inactive form's unsaved edits are dropped on switch
           exactly as on desktop. */
        <>
          <Select<SettingsTabKey>
            value={activeTab}
            onChange={(k) => setActiveTab(k)}
            options={items.map((i) => ({ value: i.key as SettingsTabKey, label: i.label }))}
            style={{ width: "100%", marginBottom: 16 }}
            size="large"
            aria-label="Settings section"
          />
          {items.find((i) => i.key === activeTab)?.children}
        </>
      )}
    </div>
  );
};
