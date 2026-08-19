// UserLimitsCard — JAB-12. Admin view + editor for a single user's resource
// limits on the AdminUserOverview detail page. Surfaces the shipped CLI/API
// (`jabali limits …`, PUT/DELETE /users/:id/limit-overrides) in the GUI:
//
//   - package limits (inherited), the per-user override, and the resolved
//     effective bundle side by side, so an operator can see where each value
//     comes from (inherited vs explicit override, including explicit-unlimited);
//   - live disk usage from the agent's user.limits.report;
//   - set / clear overrides for all six resources.
//
// Writes go to the override row only. The reconciler converges the live
// cgroup/quota state on its next tick (DB is the source of truth), so there is
// no separate "apply" button here — a saved override takes effect shortly after.
import { useState } from "react";
import { Button, Card, Drawer, Form, InputNumber, Popconfirm, Space, Table, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import type { ColumnsType } from "antd/es/table";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { HddOutlined, EditOutlined } from "@icons";

import { apiClient } from "../../../apiClient";

// The six limit fields, with the effective (PascalCase, legacy contract) and
// override/package (snake_case) JSON keys, plus display metadata.
type FieldKey =
  | "disk_quota_mb"
  | "cpu_quota_percent"
  | "memory_limit_mb"
  | "io_read_mbps"
  | "io_write_mbps"
  | "max_tasks";

interface FieldSpec {
  key: FieldKey;
  effectiveKey:
    | "DiskQuotaMB"
    | "CPUQuotaPercent"
    | "MemoryLimitMB"
    | "IOReadMbps"
    | "IOWriteMbps"
    | "MaxTasks";
  label: string;
  unit: string;
  max: number;
}

const FIELDS: FieldSpec[] = [
  { key: "disk_quota_mb", effectiveKey: "DiskQuotaMB", label: "Disk quota", unit: "MB", max: 4_194_304 },
  { key: "cpu_quota_percent", effectiveKey: "CPUQuotaPercent", label: "CPU quota", unit: "%", max: 6400 },
  { key: "memory_limit_mb", effectiveKey: "MemoryLimitMB", label: "Memory", unit: "MB", max: 4_194_304 },
  { key: "io_read_mbps", effectiveKey: "IOReadMbps", label: "IO read", unit: "MB/s", max: 100_000 },
  { key: "io_write_mbps", effectiveKey: "IOWriteMbps", label: "IO write", unit: "MB/s", max: 100_000 },
  { key: "max_tasks", effectiveKey: "MaxTasks", label: "Max tasks", unit: "", max: 1_000_000 },
];

type LimitFields = Partial<Record<FieldKey, number>>;

type UsageResponse = {
  effective: Record<FieldSpec["effectiveKey"], number>;
  package?: LimitFields | null;
  override?: LimitFields | null;
  current?: { disk?: { used_kb?: number; limit_kb?: number } };
};

function fmtValue(v: number | undefined, unit: string): string {
  if (v === undefined) return "—";
  if (v === 0) return "Unlimited";
  return unit ? `${v.toLocaleString()} ${unit}` : v.toLocaleString();
}

type Row = {
  key: FieldKey;
  label: string;
  unit: string;
  pkg?: number;
  override?: number;
  effective: number;
  source: "override" | "package" | "default";
};

export function UserLimitsCard({ userId }: { userId: string }) {
  const qc = useQueryClient();
  const [editOpen, setEditOpen] = useState(false);

  const { data, isLoading } = useQuery({
    queryKey: ["user-usage", userId],
    queryFn: async () => {
      const resp = await apiClient.get<UsageResponse>(`/users/${userId}/usage`);
      return resp.data;
    },
    refetchOnWindowFocus: false,
  });

  const clearMutation = useMutation({
    mutationFn: async () => {
      await apiClient.delete(`/users/${userId}/limit-overrides`);
    },
    onSuccess: async () => {
      feedback.message.success("Overrides cleared — reconciler will apply shortly");
      await qc.invalidateQueries({ queryKey: ["user-usage", userId] });
    },
    onError: () => feedback.message.error("Failed to clear overrides"),
  });

  const measureMutation = useMutation({
    mutationFn: async () => {
      await apiClient.post(`/users/${userId}/usage/measure-disk`);
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["user-usage", userId] });
    },
    onError: () => feedback.message.error("Disk measurement failed"),
  });

  const rows: Row[] = FIELDS.map((f) => {
    const pkg = data?.package?.[f.key];
    const override = data?.override?.[f.key];
    const effective = data?.effective?.[f.effectiveKey] ?? 0;
    const source: Row["source"] =
      override !== undefined ? "override" : pkg !== undefined && pkg !== 0 ? "package" : "default";
    return { key: f.key, label: f.label, unit: f.unit, pkg, override, effective, source };
  });

  const columns: ColumnsType<Row> = [
    { title: "Resource", dataIndex: "label", key: "label" },
    {
      title: "Package",
      key: "pkg",
      render: (_, r) => (
        <Typography.Text type="secondary">{fmtValue(r.pkg, r.unit)}</Typography.Text>
      ),
    },
    {
      title: "Override",
      key: "override",
      render: (_, r) =>
        r.override !== undefined ? (
          <Typography.Text strong>{fmtValue(r.override, r.unit)}</Typography.Text>
        ) : (
          <Typography.Text type="secondary">—</Typography.Text>
        ),
    },
    {
      title: "Effective",
      key: "effective",
      render: (_, r) => <Typography.Text>{fmtValue(r.effective, r.unit)}</Typography.Text>,
    },
    {
      title: "Source",
      key: "source",
      render: (_, r) =>
        r.source === "override" ? (
          <Tag color="gold">Override</Tag>
        ) : r.source === "package" ? (
          <Tag color="blue">Package</Tag>
        ) : (
          <Tooltip title="No package value and no override — unlimited">
            <Tag>Default</Tag>
          </Tooltip>
        ),
    },
  ];

  const diskUsed = data?.current?.disk?.used_kb;
  const hasOverride = data?.override != null && Object.keys(data.override).length > 0;

  return (
    <Card
      title={
        <Space>
          <HddOutlined />
          <span>Resource limits</span>
        </Space>
      }
      extra={
        <Space>
          <Button icon={<EditOutlined />} onClick={() => setEditOpen(true)}>
            Edit overrides
          </Button>
          <Popconfirm
            title="Clear all overrides?"
            description="The user falls back to package limits."
            okText="Clear"
            okButtonProps={{ danger: true }}
            disabled={!hasOverride}
            onConfirm={() => clearMutation.mutate()}
          >
            <Button danger disabled={!hasOverride} loading={clearMutation.isPending}>
              Clear overrides
            </Button>
          </Popconfirm>
        </Space>
      }
    >
      <Table<Row>
        rowKey="key"
        size="small"
        loading={isLoading}
        columns={columns}
        dataSource={rows}
        pagination={false}
      />
      {diskUsed !== undefined && diskUsed > 0 ? (
        <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
          Live disk usage: {(diskUsed / 1024).toFixed(0)} MB
        </Typography.Paragraph>
      ) : (
        // No quota answer on this host (quotas off, or the user genuinely
        // empty). Nothing walks the disk automatically anymore — the old
        // automatic `du` fallback IO-starved a box at peak — so offer a
        // one-click, agent-side-throttled measurement instead.
        <Space style={{ marginTop: 12 }}>
          <Typography.Text type="secondary">Disk usage: —</Typography.Text>
          <Tooltip title="Runs a one-time size scan on the server (low IO priority). Only needed when filesystem quotas are off.">
            <Button size="small" loading={measureMutation.isPending} onClick={() => measureMutation.mutate()}>
              Measure
            </Button>
          </Tooltip>
        </Space>
      )}

      <EditOverridesDrawer
        open={editOpen}
        userId={userId}
        initial={data?.override ?? {}}
        onClose={() => setEditOpen(false)}
        onSaved={() => qc.invalidateQueries({ queryKey: ["user-usage", userId] })}
      />
    </Card>
  );
}

function EditOverridesDrawer({
  open,
  userId,
  initial,
  onClose,
  onSaved,
}: {
  open: boolean;
  userId: string;
  initial: LimitFields;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [form] = Form.useForm<LimitFields>();

  const saveMutation = useMutation({
    mutationFn: async (values: LimitFields) => {
      // PUT replaces the whole override row. An empty field means "inherit"
      // (send null); a number (including 0 = explicit unlimited) is an override.
      const payload: Record<FieldKey, number | null> = {
        disk_quota_mb: null,
        cpu_quota_percent: null,
        memory_limit_mb: null,
        io_read_mbps: null,
        io_write_mbps: null,
        max_tasks: null,
      };
      for (const f of FIELDS) {
        const v = values[f.key];
        payload[f.key] = v === undefined || v === null ? null : v;
      }
      await apiClient.put(`/users/${userId}/limit-overrides`, payload);
    },
    onSuccess: () => {
      feedback.message.success("Overrides saved — reconciler will apply shortly");
      onSaved();
      onClose();
    },
    onError: (err: unknown) => {
      const detail =
        (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail;
      feedback.message.error(detail ? `Validation failed: ${detail}` : "Failed to save overrides");
    },
  });

  return (
    <Drawer
      title="Edit resource-limit overrides"
      open={open}
      onClose={onClose}
      width={420}
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            type="primary"
            loading={saveMutation.isPending}
            onClick={() => form.submit()}
          >
            Save
          </Button>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary">
        Leave a field empty to inherit the package value. Enter <b>0</b> for
        explicit unlimited. Saved values take effect on the next reconcile tick.
      </Typography.Paragraph>
      <Form
        form={form}
        layout="vertical"
        initialValues={initial}
        onFinish={(v) => saveMutation.mutate(v)}
      >
        {FIELDS.map((f) => (
          <Form.Item
            key={f.key}
            name={f.key}
            label={`${f.label}${f.unit ? ` (${f.unit})` : ""}`}
          >
            <InputNumber
              min={0}
              max={f.max}
              step={1}
              style={{ width: "100%" }}
              placeholder="inherit from package"
            />
          </Form.Item>
        ))}
      </Form>
    </Drawer>
  );
}
