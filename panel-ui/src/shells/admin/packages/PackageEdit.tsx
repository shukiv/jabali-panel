// PackageEdit — admin form for updating a hosting package.
//
// Same field set as PackageCreate; initial values load via useOneQuery
// and seed the Form once they arrive.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Button, Card, Col, Divider, Form, Input, InputNumber, Row, Select, Spin, Switch, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { CheckOutlined, CloseOutlined } from "@icons";
import { useNavigate, useParams } from "react-router";

import { apiClient } from "../../../apiClient";
import { useOneQuery, useUpdateMutation } from "../../../hooks/useQueries";
import { useDiskQuotaEnabled } from "../../../hooks/useDiskQuotaEnabled";

type NspawnImage = { name: string };

// Mirrors models.AllBackupDestinationKinds (GH #454). Keep in sync with the
// backend enum in backup_destination.go.
const BACKUP_DESTINATION_KINDS = ["local", "sftp", "s3", "b2", "azure", "gcs", "rest"] as const;

type PackageEditInput = {
  name: string;
  disk_quota_mb: number;
  // M18 — per-user resource limits. 0 means unlimited on every field.
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
  max_ftp_accounts: number;
  // Tenant backup limits (GH #454). allowed_backup_destination_kinds is a CSV
  // string on the wire; the multi-Select binds an array (converted on load/save,
  // like docker_app_slugs).
  max_backups: number;
  max_backup_schedules: number;
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
  docker_app_slugs?: string[] | string;
  nspawn_image_version?: string | null;
};

type PackageRecord = PackageEditInput & { id: string };

export const PackageEdit = () => {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [form] = Form.useForm<PackageEditInput>();
  const { enabled: diskQuotaEnabled } = useDiskQuotaEnabled();

  const { data, isLoading } = useOneQuery<PackageRecord>({
    resource: "packages",
    id,
  });
  const updateMutation = useUpdateMutation<PackageRecord, PackageEditInput>({
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
        // empty list — Select stays empty, server default is used
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

  useEffect(() => {
    if (data) {
      const { id: _id, ...rest } = data;
      void _id;
      // docker_app_slugs is a CSV string on the wire; the Select needs an array.
      const csv = typeof rest.docker_app_slugs === "string" ? rest.docker_app_slugs : "";
      // allowed_backup_destination_kinds (GH #454) is CSV too.
      const bkCsv = typeof rest.allowed_backup_destination_kinds === "string" ? rest.allowed_backup_destination_kinds : "";
      form.setFieldsValue({
        ...rest,
        docker_app_slugs: csv ? csv.split(",").filter(Boolean) : [],
        allowed_backup_destination_kinds: bkCsv ? bkCsv.split(",").filter(Boolean) : [],
      });
    }
  }, [data, form]);

  const handleFinish = async (values: PackageEditInput) => {
    if (!id) return;
    try {
      const input = {
        ...values,
        docker_app_slugs: Array.isArray(values.docker_app_slugs) ? values.docker_app_slugs.join(",") : (values.docker_app_slugs ?? ""),
        allowed_backup_destination_kinds: Array.isArray(values.allowed_backup_destination_kinds)
          ? values.allowed_backup_destination_kinds.join(",")
          : (values.allowed_backup_destination_kinds ?? ""),
      } as unknown as PackageEditInput;
      await updateMutation.mutateAsync({ id, input });
      feedback.message.success("Package updated");
      navigate("/jabali-admin/packages");
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to update package";
      feedback.message.error(msg);
    }
  };

  if (isLoading && !data) {
    return (
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          minHeight: 240,
        }}
      >
        <Spin />
      </div>
    );
  }

  return (
    <Card>
      <Typography.Title level={3} style={{ marginTop: 0 }}>
        Edit package
      </Typography.Title>
      <Form<PackageEditInput>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
      >
        <Form.Item
          label={t("packageedit.name")}
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
              label={t("packageedit.disk_quota_mb")}
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
              label={t("packageedit.cpu_quota")}
              name="cpu_quota_percent"
              tooltip="systemd CPUQuota — 100% = 1 core, 200% = 2 cores. 0 = unlimited."
            >
              <InputNumber min={0} max={10000} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.memory_limit_mb")}
              name="memory_limit_mb"
              tooltip="systemd MemoryMax; MemoryHigh is fixed at 90% of this. 0 = unlimited."
            >
              <InputNumber min={0} max={1048576} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.io_read_bandwidth_mb_s")}
              name="io_read_mbps"
              tooltip="systemd IOReadBandwidthMax on /. 0 = unlimited."
            >
              <InputNumber min={0} max={10000} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.io_write_bandwidth_mb_s")}
              name="io_write_mbps"
              tooltip="systemd IOWriteBandwidthMax on /. 0 = unlimited."
            >
              <InputNumber min={0} max={10000} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.max_tasks")}
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
              label={t("packageedit.bandwidth_quota_mb")}
              name="bandwidth_quota_mb"
              rules={[{ required: true, message: "Bandwidth quota is required" }]}
              tooltip="0 = unlimited"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.max_domains")}
              name="max_domains"
              rules={[{ required: true, message: "Max domains is required" }]}
              tooltip="0 = unlimited"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.max_email_accounts")}
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
              label={t("packageedit.max_databases")}
              name="max_databases"
              rules={[{ required: true, message: "Max databases is required" }]}
              tooltip="0 = unlimited"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.max_docker_apps")}
              name="max_docker_apps"
              tooltip="0 = Docker apps not included in this package"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.max_python_apps")}
              name="max_python_apps"
              tooltip="0 = Python apps not included in this package"
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.max_ftp_accounts")}
              name="max_ftp_accounts"
              tooltip="0 = FTP/SFTP accounts not included in this package"
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
              label={t("packageedit.max_backups")}
              name="max_backups"
              tooltip={t("packageedit.retention_cap_most_snapshots_a_tenant_on_thi")}
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.scheduled_backups")}
              name="scheduled_backups_enabled"
              valuePropName="checked"
              tooltip={t("packageedit.allow_tenants_on_this_plan_to_enable_a_sched")}
            >
              <Switch />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.max_backup_schedules")}
              name="max_backup_schedules"
              tooltip={t("packageedit.how_many_scheduled_backups_a_tenant_may_own")}
            >
              <InputNumber min={1} style={{ width: "100%" }} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={8}>
            <Form.Item
              label={t("packageedit.allowed_backup_destinations")}
              name="allowed_backup_destination_kinds"
              tooltip={t("packageedit.destination_kinds_a_tenant_may_back_up_to_em")}
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
              label={t("packageedit.backup_retention_policy")}
              name="backup_retention_policy"
              tooltip={t("packageedit.what_happens_when_a_tenant_reaches_max_backu")}
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
            tooltip={t("packageedit.allow_ssh_access")}
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
            tooltip={t("packageedit.allow_cgi_scripts")}
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
            tooltip={t("packageedit.security_re_enables_php_exec_proc_open_shell")}
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
            tooltip={t("packageedit.let_tenants_pick_a_safe_php_performance_mode")}
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
            tooltip={t("packageedit.also_expose_the_individual_pm_knobs_clamped")}
            noStyle
          >
            <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
          </Form.Item>
          <Typography.Text>Users can use Advanced mode (raw pm.*, clamped to the cap)</Typography.Text>
        </div>
        <Form.Item name="fpm_max_children_cap" label={t("packageedit.max_children_per_user_fpm_cap")}>
          <InputNumber min={1} max={2000} style={{ width: 160 }} />
        </Form.Item>
        <Form.Item name="fpm_worker_mem_mb" label={t("packageedit.est_ram_per_worker_mb_drives_the_memory_budg")}>
          <InputNumber min={8} max={2048} style={{ width: 160 }} />
        </Form.Item>


        <Form.Item
          label={t("packageedit.docker_apps_per_package_allowlist")}
          name="docker_app_slugs"
          extra="Tenants on this package may install only these apps. Empty = use the server-wide Docker Apps curation. Requires Max Docker Apps > 0."
        >
          <Select
            mode="multiple"
            allowClear
            placeholder={t("packageedit.empty_server_wide_default")}
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
            loading={updateMutation.isPending}
          >
            Save
          </Button>
        </Form.Item>
      </Form>
    </Card>
  );
};
