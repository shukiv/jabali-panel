// PackageCreate — admin form for a new hosting package.
//
// Form.useForm + useCreateMutation; layout matches the old Refine
// version (grid of resource limits + feature quotas + two switches).
import { useTranslation } from "react-i18next";
import {
  Button,
  Card,
  Col,
  Divider,
  Form,
  Input,
  InputNumber,
  Row,
  Select,
  Switch,
  Typography,
  message,
} from "antd";
import { CheckOutlined, CloseOutlined } from "@icons";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router";

import { apiClient } from "../../../apiClient";
import { useCreateMutation } from "../../../hooks/useQueries";
import { useDiskQuotaEnabled } from "../../../hooks/useDiskQuotaEnabled";

type NspawnImage = { name: string };

// Mirrors models.AllBackupDestinationKinds (GH #454).
const BACKUP_DESTINATION_KINDS = ["local", "sftp", "s3", "b2", "azure", "gcs", "rest"] as const;

type PackageCreateInput = {
  name: string;
  disk_quota_mb: number;
  cpu_quota_percent: number;
  memory_limit_mb: number;
  io_read_mbps: number;
  io_write_mbps: number;
  max_tasks: number;
  bandwidth_quota_mb: number;
  max_domains: number;
  max_email_accounts: number;
  max_databases: number;
  max_docker_apps: number;
  max_python_apps: number;
  // Tenant backup limits (GH #454).
  max_backups: number;
  scheduled_backups_enabled: boolean;
  allowed_backup_destination_kinds: string | string[];
  backup_retention_policy: string;
  ssh_enabled: boolean;
  cgi_enabled: boolean;
  php_exec_enabled: boolean;
  fpm_user_can_edit: boolean;
  fpm_advanced_mode: boolean;
  fpm_max_children_cap: number;
  fpm_worker_mem_mb: number;
  docker_app_slugs?: string[];
  nspawn_image_version?: string | null;
};

type PackageCreated = { id: string };

export const PackageCreate = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [form] = Form.useForm<PackageCreateInput>();
  const { enabled: diskQuotaEnabled } = useDiskQuotaEnabled();
  const createMutation = useCreateMutation<PackageCreated, PackageCreateInput>({
    resource: "packages",
  });

  const [nspawnImages, setNspawnImages] = useState<NspawnImage[]>([]);
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ images: NspawnImage[] }>(
          "/system/nspawn-images",
        );
        if (!cancelled) setNspawnImages(resp.data.images || []);
      } catch {
        // empty list — Select stays empty
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const [dockerApps, setDockerApps] = useState<{ slug: string; name: string }[]>([]);
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const r = await apiClient.get<{ items: { slug: string; name: string; tenant_installable: boolean }[] }>(
          "/admin/docker-apps/catalog",
        );
        if (!cancelled) setDockerApps((r.data.items || []).filter((e) => e.tenant_installable).map((e) => ({ slug: e.slug, name: e.name })));
      } catch {
        /* catalog unavailable */
      }
    })();
    return () => { cancelled = true; };
  }, []);

  const handleFinish = async (values: PackageCreateInput) => {
    try {
      const payload = {
        ...values,
        docker_app_slugs: Array.isArray(values.docker_app_slugs) ? values.docker_app_slugs.join(",") : "",
        allowed_backup_destination_kinds: Array.isArray(values.allowed_backup_destination_kinds)
          ? values.allowed_backup_destination_kinds.join(",")
          : (values.allowed_backup_destination_kinds ?? ""),
      } as unknown as PackageCreateInput;
      await createMutation.mutateAsync(payload);
      message.success("Package created");
      navigate("/jabali-admin/packages");
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to create package";
      message.error(msg);
    }
  };

  return (
    <Card>
      <Typography.Title level={3} style={{ marginTop: 0 }}>
        Create package
      </Typography.Title>
      <Form<PackageCreateInput>
        form={form}
        layout="vertical"
        initialValues={{
          ssh_enabled: false,
          cgi_enabled: false,
          php_exec_enabled: false,
          fpm_user_can_edit: false,
          fpm_advanced_mode: false,
          fpm_max_children_cap: 20,
          fpm_worker_mem_mb: 64,
          disk_quota_mb: 0,
          cpu_quota_percent: 0,
          memory_limit_mb: 0,
          io_read_mbps: 0,
          io_write_mbps: 0,
          max_tasks: 0,
          bandwidth_quota_mb: 0,
          max_domains: 0,
          max_email_accounts: 0,
          max_databases: 0,
          max_docker_apps: 0,
          max_python_apps: 0,
          max_backups: 0,
          scheduled_backups_enabled: false,
          allowed_backup_destination_kinds: [],
          backup_retention_policy: "reject",
        }}
        onFinish={handleFinish}
      >
        <Form.Item
          label={t("packagecreate.name")}
          name="name"
          rules={[{ required: true, message: "Package name is required" }]}
        >
          <Input placeholder="e.g., Basic, Professional, Enterprise" />
        </Form.Item>

        <Divider titlePlacement="left">Resource limits</Divider>
        <Typography.Paragraph type="secondary" style={{ marginTop: -8 }}>
          Enforced per-user via POSIX quota (disk) and cgroups v2
          (cpu/memory/io/tasks). Zero on any field means unlimited.
        </Typography.Paragraph>

        <Row gutter={16}>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.disk_quota_mb")}
              name="disk_quota_mb"
              rules={[{ required: true, message: "Disk quota is required" }]}
              tooltip={
                diskQuotaEnabled
                  ? "Hard limit enforced via setquota(8). 0 = unlimited."
                  : "Disabled — enable POSIX disk quotas in Server Settings → Disk Quotas first."
              }
              extra={
                diskQuotaEnabled
                  ? undefined
                  : "Disabled until disk quotas are enabled in Server Settings."
              }
            >
              <InputNumber min={0} style={{ width: "100%" }} disabled={!diskQuotaEnabled} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.cpu_quota")}
              name="cpu_quota_percent"
              tooltip="systemd CPUQuota — 100% = 1 core, 200% = 2 cores. 0 = unlimited."
            >
              <InputNumber min={0} max={10000} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.memory_limit_mb")}
              name="memory_limit_mb"
              tooltip="systemd MemoryMax; MemoryHigh is fixed at 90% of this. 0 = unlimited."
            >
              <InputNumber min={0} max={1048576} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.io_read_bandwidth_mb_s")}
              name="io_read_mbps"
              tooltip="systemd IOReadBandwidthMax on /. 0 = unlimited."
            >
              <InputNumber min={0} max={10000} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.io_write_bandwidth_mb_s")}
              name="io_write_mbps"
              tooltip="systemd IOWriteBandwidthMax on /. 0 = unlimited."
            >
              <InputNumber min={0} max={10000} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.max_tasks")}
              name="max_tasks"
              tooltip="systemd TasksMax — upper bound on concurrent processes. 0 = unlimited."
            >
              <InputNumber min={0} max={100000} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
        </Row>

        <Divider titlePlacement="left">Feature quotas</Divider>

        <Row gutter={16}>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.bandwidth_quota_mb")}
              name="bandwidth_quota_mb"
              rules={[{ required: true, message: "Bandwidth quota is required" }]}
              tooltip="0 = unlimited"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.max_domains")}
              name="max_domains"
              rules={[{ required: true, message: "Max domains is required" }]}
              tooltip="0 = unlimited"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.max_email_accounts")}
              name="max_email_accounts"
              rules={[
                { required: true, message: "Max email accounts is required" },
              ]}
              tooltip="0 = unlimited"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.max_databases")}
              name="max_databases"
              rules={[{ required: true, message: "Max databases is required" }]}
              tooltip="0 = unlimited"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.max_docker_apps")}
              name="max_docker_apps"
              tooltip="0 = Docker apps not included in this package"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.max_python_apps")}
              name="max_python_apps"
              tooltip="0 = Python apps not included in this package"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
        </Row>

        <Divider titlePlacement="left">Backups</Divider>
        <Typography.Paragraph type="secondary" style={{ marginTop: -8 }}>
          Tenant self-service backups (GH #454). The admin owns the schedule time;
          tenants choose what to back up and which allowed destination, within these
          limits. Max backups = 0 disables tenant backups entirely.
        </Typography.Paragraph>
        <Row gutter={16}>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.max_backups")}
              name="max_backups"
              tooltip={t("packagecreate.retention_cap_most_snapshots_a_tenant_on_thi")}
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.scheduled_backups")}
              name="scheduled_backups_enabled"
              valuePropName="checked"
              tooltip={t("packagecreate.allow_tenants_on_this_plan_to_enable_a_sched")}
            >
              <Switch />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.allowed_backup_destinations")}
              name="allowed_backup_destination_kinds"
              tooltip={t("packagecreate.destination_kinds_a_tenant_may_back_up_to_em")}
            >
              <Select
                mode="multiple"
                allowClear
                placeholder="e.g. local, s3"
                options={BACKUP_DESTINATION_KINDS.map((k) => ({ value: k, label: k }))}
                style={{ width: "100%" }}
              />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packagecreate.backup_retention_policy")}
              name="backup_retention_policy"
              tooltip={t("packagecreate.what_happens_when_a_tenant_reaches_max_backu")}
            >
              <Select
                options={[
                  { value: "reject", label: "Reject at cap (safe)" },
                  { value: "prune", label: "Auto-prune oldest" },
                ]}
                style={{ width: "100%" }}
              />
            </Form.Item>
          </Col>
        </Row>

        <Divider titlePlacement="left">Features</Divider>

        <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12 }}>
          <Form.Item
            name="ssh_enabled"
            valuePropName="checked"
            tooltip={t("packagecreate.allow_ssh_access")}
            noStyle
          >
            <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
          </Form.Item>
          <Typography.Text>SSH Enabled</Typography.Text>
        </div>

        <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 24 }}>
          <Form.Item
            name="cgi_enabled"
            valuePropName="checked"
            tooltip={t("packagecreate.allow_cgi_scripts")}
            noStyle
          >
            <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
          </Form.Item>
          <Typography.Text>CGI Enabled</Typography.Text>
        </div>

        <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 24 }}>
          <Form.Item
            name="php_exec_enabled"
            valuePropName="checked"
            tooltip={t("packagecreate.security_re_enables_php_exec_proc_open_shell")}
            noStyle
          >
            <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
          </Form.Item>
          <Typography.Text>Allow PHP exec functions <Typography.Text type="warning">(proc_open / shell_exec — security risk)</Typography.Text></Typography.Text>
        </div>

        <Typography.Title level={5} style={{ marginTop: 8 }}>
          PHP-FPM Performance Policy
        </Typography.Title>
        <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12 }}>
          <Form.Item
            name="fpm_user_can_edit"
            valuePropName="checked"
            tooltip={t("packagecreate.let_tenants_pick_a_safe_php_performance_mode")}
            noStyle
          >
            <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
          </Form.Item>
          <Typography.Text>Users can pick a PHP Performance Mode</Typography.Text>
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12 }}>
          <Form.Item
            name="fpm_advanced_mode"
            valuePropName="checked"
            tooltip={t("packagecreate.also_expose_the_individual_pm_knobs_clamped")}
            noStyle
          >
            <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
          </Form.Item>
          <Typography.Text>Users can use Advanced mode (raw pm.*, clamped to the cap)</Typography.Text>
        </div>
        <Form.Item name="fpm_max_children_cap" label={t("packagecreate.max_children_per_user_fpm_cap")}>
          <InputNumber min={1} max={2000} style={{ width: 160 }} />
        </Form.Item>
        <Form.Item name="fpm_worker_mem_mb" label={t("packagecreate.est_ram_per_worker_mb_drives_the_memory_budg")}>
          <InputNumber min={8} max={2048} style={{ width: 160 }} />
        </Form.Item>


        <Form.Item
          label={t("packagecreate.docker_apps_per_package_allowlist")}
          name="docker_app_slugs"
          extra="Tenants on this package may install only these apps. Empty = use the server-wide Docker Apps curation. Requires Max Docker Apps > 0."
        >
          <Select
            mode="multiple"
            allowClear
            placeholder={t("packagecreate.empty_server_wide_default")}
            options={dockerApps.map((a) => ({ value: a.slug, label: a.name }))}
          />
        </Form.Item>

        <Form.Item
          label="nspawn sandbox image"
          name="nspawn_image_version"
          extra="Only used when sandbox mode = nspawn AND SSH is enabled. Empty = use server default."
        >
          <Select
            showSearch
            allowClear
            placeholder="(use server default)"
            options={nspawnImages.map((img) => ({ value: img.name, label: img.name }))}
            style={{ width: "100%" }}
          />
        </Form.Item>

        <Form.Item>
          <Button
            type="primary"
            htmlType="submit"
            loading={createMutation.isPending}
          >
            Save
          </Button>
        </Form.Item>
      </Form>
    </Card>
  );
};
