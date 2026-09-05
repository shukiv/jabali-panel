// PackageEditor — the shared Hosting Package entitlement form (JAB-331).
//
// One deep Module that both PackageCreate and PackageEdit render through. It owns
// the whole Form (fields, layout, validation), the two catalog fetches (nspawn
// images + tenant-installable Docker apps), the disk-quota toggle, and the CSV
// codecs. The shells stay thin adapters: they own the mutation, navigation, and
// the success/error toast, handed in via `onSubmit`.
//
// Create vs edit is a data distinction, not a code fork:
//   - create: no `initialValue` → the Form seeds from PACKAGE_DEFAULTS.
//   - edit:   `initialValue` arrives from the query → seeded once (a later
//             background refetch must not clobber the operator's unsaved edits).
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button, Card, Col, Divider, Form, Input, InputNumber, Row, Select, Spin, Switch, Typography } from "antd";
import { CheckOutlined, CloseOutlined } from "@icons";

import { apiClient } from "../../apiClient";
import { useDiskQuotaEnabled } from "../../hooks/useDiskQuotaEnabled";
import {
  BACKUP_DESTINATION_KINDS,
  PACKAGE_DEFAULTS,
  PACKAGE_LIMIT_FIELDS,
  decodePackageForm,
  encodePackagePayload,
  type LimitFieldDef,
  type PackageFormValues,
  type PackageRecord,
  type PackageWirePayload,
} from "./packageFields";

type NspawnImage = { name: string };

type TFunc = ReturnType<typeof useTranslation>["t"];

export type PackageEditorProps = {
  title: string;
  // Edit: the loaded record. Create: undefined/null.
  initialValue?: PackageRecord | null;
  // Edit: the record query is still loading.
  isLoading?: boolean;
  // The adapter's mutation is in flight.
  submitting: boolean;
  // The adapter owns mutate + toast + navigate + error handling; it must not
  // throw (the Module does not re-toast).
  onSubmit: (payload: PackageWirePayload) => Promise<void>;
};

// One numeric entitlement field, driven by its LimitFieldDef.
function LimitField({ field, t }: { field: LimitFieldDef; t: TFunc }) {
  const tooltip = field.tooltipKey ? t(`packageedit.${field.tooltipKey}`) : field.tooltipText;
  return (
    <Form.Item
      label={t(`packageedit.${field.labelKey}`)}
      name={field.name}
      rules={field.required ? [{ required: true, message: field.requiredMsg }] : undefined}
      tooltip={tooltip}
    >
      <InputNumber min={field.min} max={field.max} style={{ width: field.width }} />
    </Form.Item>
  );
}

// Only a name that is actually in PACKAGE_LIMIT_FIELDS is accepted, so a typo or
// an unlisted key (e.g. a switch field) is a compile error, not a runtime crash.
type LimitFieldName = (typeof PACKAGE_LIMIT_FIELDS)[number]["name"];

const byGroup = (group: LimitFieldDef["group"]): readonly LimitFieldDef[] =>
  PACKAGE_LIMIT_FIELDS.filter((f) => f.group === group);
const byName = (name: LimitFieldName): LimitFieldDef =>
  PACKAGE_LIMIT_FIELDS.find((f) => f.name === name) as LimitFieldDef;

export const PackageEditor = ({ title, initialValue, isLoading, submitting, onSubmit }: PackageEditorProps) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<PackageFormValues>();
  const { enabled: diskQuotaEnabled } = useDiskQuotaEnabled();

  const [nspawnImages, setNspawnImages] = useState<NspawnImage[]>([]);
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ images: NspawnImage[] }>("/system/nspawn-images");
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
        if (!cancelled)
          setDockerApps((r.data.items || []).filter((e) => e.tenant_installable).map((e) => ({ slug: e.slug, name: e.name })));
      } catch {
        /* catalog unavailable */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // Seed the edit form once per record. Keying on `[data, form]` (the old
  // behaviour) re-seeded on every background refetch and discarded the operator's
  // unsaved edits. Tracking the seeded id (not a boolean) also reseeds correctly
  // if the same element is ever reused for a different package.
  const seededIdRef = useRef<string | null>(null);
  useEffect(() => {
    if (initialValue && seededIdRef.current !== initialValue.id) {
      seededIdRef.current = initialValue.id;
      form.setFieldsValue(decodePackageForm(initialValue));
    }
  }, [initialValue, form]);

  const handleFinish = (values: PackageFormValues) => onSubmit(encodePackagePayload(values));

  if (isLoading && !initialValue) {
    return (
      <div style={{ display: "flex", alignItems: "center", justifyContent: "center", minHeight: 240 }}>
        <Spin />
      </div>
    );
  }

  return (
    <Card>
      <Typography.Title level={3} style={{ marginTop: 0 }}>
        {title}
      </Typography.Title>
      <Form<PackageFormValues>
        form={form}
        layout="vertical"
        initialValues={initialValue ? undefined : PACKAGE_DEFAULTS}
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
          Enforced per-user via POSIX quota (disk) and cgroups v2 (cpu/memory/io/tasks). Zero on any field means unlimited.
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
              extra={diskQuotaEnabled ? undefined : "Disabled until disk quotas are enabled in Server Settings."}
            >
              <InputNumber min={0} style={{ width: "100%" }} disabled={!diskQuotaEnabled} />
            </Form.Item>
          </Col>
          {byGroup("resource").map((f) => (
            <Col key={f.name} xs={24} sm={12} md={8}>
              <LimitField field={f} t={t} />
            </Col>
          ))}
        </Row>

        <Divider titlePlacement="left">Feature quotas</Divider>

        <Row gutter={16}>
          {byGroup("quota").map((f) => (
            <Col key={f.name} xs={24} sm={12} md={8}>
              <LimitField field={f} t={t} />
            </Col>
          ))}
        </Row>

        <Divider titlePlacement="left">Backups</Divider>
        <Typography.Paragraph type="secondary" style={{ marginTop: -8 }}>
          Tenant self-service backups (GH #454). The admin owns the schedule time; tenants choose what to back up and which
          allowed destination, within these limits. Max backups = 0 disables tenant backups entirely.
        </Typography.Paragraph>
        <Row gutter={16}>
          <Col xs={24} sm={12} md={8}>
            <LimitField field={byName("max_backups")} t={t} />
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
            <LimitField field={byName("max_backup_schedules")} t={t} />
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
          <Form.Item name="ssh_enabled" valuePropName="checked" tooltip={t("packageedit.allow_ssh_access")} noStyle>
            <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
          </Form.Item>
          <Typography.Text>SSH Enabled</Typography.Text>
        </div>

        <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 24 }}>
          <Form.Item name="cgi_enabled" valuePropName="checked" tooltip={t("packageedit.allow_cgi_scripts")} noStyle>
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
          <Typography.Text>
            Allow PHP exec functions{" "}
            <Typography.Text type="warning">(proc_open / shell_exec — security risk)</Typography.Text>
          </Typography.Text>
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
        <LimitField field={byName("fpm_max_children_cap")} t={t} />
        <LimitField field={byName("fpm_worker_mem_mb")} t={t} />

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
          <Button type="primary" htmlType="submit" loading={submitting}>
            Save
          </Button>
        </Form.Item>
      </Form>
    </Card>
  );
};
