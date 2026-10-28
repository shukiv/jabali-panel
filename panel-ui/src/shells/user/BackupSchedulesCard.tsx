// BackupSchedulesCard — tenant multi-schedule backup surface (GH #454 7B).
// A tenant on a plan with max_backup_schedules > 1 manages a LIST of backup
// schedules; each owns content + cadence + destination + retention + on/off.
// The ADMIN owns the maintenance WINDOW (shown read-only) that these fire
// inside — the tenant never picks a time, only a cadence, so no tenant can
// choose a stampede minute. On a single-schedule plan this falls back to the
// legacy MyProfileScheduleCard.
import { useTranslation } from "react-i18next";
import { Alert, Button, Card, Drawer, Form, InputNumber, Popconfirm, Select, Space, Switch, Table, Tag, Typography } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { ClockCircleOutlined, DeleteOutlined, EditOutlined, PlusOutlined } from "@icons";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import { extractApiError } from "../../apiErrors";
import { MyProfileScheduleCard } from "./MyProfileScheduleCard";
import { Timestamp, usePanelClock } from "../../components/Timestamp";
// The same lists the admin Schedules tab uses for these schedules (GH #2246).
import {
  BACKUP_CADENCE_OPTIONS as CADENCE_OPTIONS,
  BACKUP_CONTENT_OPTIONS as CONTENT_OPTIONS,
  backupCadenceLabel as cadenceLabel,
  backupContentLabel as contentLabel,
  cadenceRunsPerWeek,
  keptBackups,
  maxKeepDays,
} from "../../lib/backupScheduleOptions";

interface ScheduleRow {
  id: string;
  enabled: boolean;
  content: string;
  cadence: string;
  destination_id?: string;
  keep_daily?: number | null;
  // Days to keep: every backup of the last N days (GH #2300).
  keep_within_days?: number | null;
  next_run_at?: string | null;
  last_run_at?: string | null;
}

interface SchedulesView {
  schedules: ScheduleRow[];
  max_backup_schedules: number;
  max_backups: number;
  scheduled_backups_enabled: boolean;
  multi_enabled: boolean;
  window_start: string;
  window_end: string;
  // GH #1097: whether the host's window actually restricts when schedules run.
  window_enforced?: boolean;
}

interface DestOption {
  id: string;
  name: string;
  kind: string;
}

interface DrawerForm {
  content: string;
  cadence: string;
  destination_id?: string;
  keep_within_days?: number | null;
  enabled: boolean;
}

export const BackupSchedulesCard = () => {
  const { t } = useTranslation();
  const tz = usePanelClock().timezone;
  const [form] = Form.useForm<DrawerForm>();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const formCadence = Form.useWatch("cadence", form);
  const formKeepDays = Form.useWatch("keep_within_days", form);

  const listQuery = useQuery({
    queryKey: ["me-backup-schedules"],
    queryFn: async () => (await apiClient.get<{ data: SchedulesView }>("/me/backup-schedules")).data.data,
  });
  const destQuery = useQuery({
    queryKey: ["me-backup-destinations"],
    queryFn: async () =>
      (await apiClient.get<{ data: DestOption[]; allow_local: boolean }>("/me/backups/destinations")).data,
  });

  const view = listQuery.data;
  const dests = destQuery.data?.data ?? [];
  const destOptions = dests.map((d) => ({ value: d.id, label: `${d.name} (${d.kind})` }));
  const destName = (id?: string) => {
    if (!id) return <Typography.Text type="secondary">—</Typography.Text>;
    const d = dests.find((x) => x.id === id);
    return d ? `${d.name} (${d.kind})` : id;
  };

  // A single-schedule plan keeps the original card — no behaviour change.
  if (view && !view.multi_enabled) {
    return <MyProfileScheduleCard />;
  }

  const openCreate = () => {
    setEditingId(null);
    form.setFieldsValue({ content: "full", cadence: "daily", destination_id: undefined, keep_within_days: null, enabled: true });
    setDrawerOpen(true);
  };
  const openEdit = (row: ScheduleRow) => {
    setEditingId(row.id);
    form.setFieldsValue({
      content: row.content || "full",
      cadence: row.cadence || "daily",
      destination_id: row.destination_id,
      keep_within_days: row.keep_within_days ?? row.keep_daily ?? null,
      enabled: row.enabled,
    });
    setDrawerOpen(true);
  };

  const submitDrawer = async () => {
    const values = await form.validateFields();
    setSaving(true);
    try {
      const body = {
        enabled: values.enabled,
        content: values.content,
        cadence: values.cadence,
        destination_id: values.destination_id ?? "",
        // Days to keep replaces the older one-per-day rule.
        keep_daily: null,
        keep_within_days: values.keep_within_days ?? null,
      };
      if (editingId) {
        await apiClient.put(`/me/backup-schedules/${editingId}`, body);
      } else {
        await apiClient.post("/me/backup-schedules", body);
      }
      feedback.message.success(editingId ? "Schedule updated" : "Schedule created");
      setDrawerOpen(false);
      listQuery.refetch();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Save failed"));
    } finally {
      setSaving(false);
    }
  };

  // Inline enable/disable — PUTs the whole row (the API replaces, not patches),
  // so carry the existing content/cadence/destination through.
  const toggleEnabled = async (row: ScheduleRow, enabled: boolean) => {
    try {
      await apiClient.put(`/me/backup-schedules/${row.id}`, {
        enabled,
        content: row.content,
        cadence: row.cadence,
        destination_id: row.destination_id ?? "",
        keep_daily: row.keep_daily ?? null,
        keep_within_days: row.keep_within_days ?? null,
      });
      listQuery.refetch();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Update failed"));
    }
  };

  const removeRow = async (id: string) => {
    try {
      await apiClient.delete(`/me/backup-schedules/${id}`);
      feedback.message.success("Schedule removed");
      listQuery.refetch();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Delete failed"));
    }
  };

  const atCap = !!view && view.schedules.length >= view.max_backup_schedules;

  // Days to keep holds every backup of those days, so how many backups that is
  // depends on how often the schedule runs; the plan's limit caps it.
  const maxBackups = view?.max_backups || 1;
  const keepDaysMax = maxKeepDays(maxBackups, cadenceRunsPerWeek(formCadence ?? "daily"));
  const keepDaysHint = (() => {
    if (!formKeepDays) return "No backups are removed by this schedule.";
    const runs = cadenceRunsPerWeek(formCadence ?? "daily");
    const n = keptBackups(formKeepDays, runs, maxBackups);
    return Math.ceil((formKeepDays * runs) / 7) > maxBackups
      ? `About ${n} backups: your plan's limit. At the limit, a run removes this schedule's oldest backup first.`
      : `About ${n} backup${n === 1 ? "" : "s"}.`;
  })();

  const columns = [
    { title: "Type", dataIndex: "content", key: "content", render: (c: string) => contentLabel(c) },
    { title: "Cadence", dataIndex: "cadence", key: "cadence", render: (c: string) => cadenceLabel(c) },
    { title: "Destination", dataIndex: "destination_id", key: "destination_id", render: (id?: string) => destName(id) },
    {
      title: "Keep",
      dataIndex: "keep_within_days",
      key: "keep_within_days",
      // Every backup of the last N days (GH #2300).
      render: (k: number | null | undefined, row: ScheduleRow) => {
        const days = k ?? row.keep_daily;
        return days ? `${days} day${days === 1 ? "" : "s"}` : <Typography.Text type="secondary">default</Typography.Text>;
      },
    },
    {
      title: "Next run",
      dataIndex: "next_run_at",
      key: "next_run_at",
      render: (n?: string | null) =>
        n ? <Timestamp value={n} /> : <Typography.Text type="secondary">—</Typography.Text>,
    },
    {
      title: "Enabled",
      dataIndex: "enabled",
      key: "enabled",
      render: (enabled: boolean, row: ScheduleRow) => (
        <Switch size="small" checked={enabled} onChange={(v) => toggleEnabled(row, v)} />
      ),
    },
    {
      title: "",
      key: "actions",
      render: (_: unknown, row: ScheduleRow) => (
        <Space>
          <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(row)} />
          <Popconfirm title="Remove this schedule?" okText="Remove" okButtonProps={{ danger: true }} onConfirm={() => removeRow(row.id)}>
            <Button size="small" danger icon={<DeleteOutlined />} />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <Card
      title={
        <span>
          <ClockCircleOutlined style={{ marginRight: 8 }} />
          Backup schedules
        </span>
      }
      style={{ marginTop: 16 }}
      loading={listQuery.isLoading}
      extra={
        <Button type="primary" icon={<PlusOutlined />} disabled={atCap} onClick={openCreate}>
          New schedule
        </Button>
      }
    >
      {view && !view.scheduled_backups_enabled ? (
        <Alert type="info" showIcon message={t("myprofileschedulecard.scheduled_backups_are_not_included_in_your_h")} />
      ) : (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            {view?.window_enforced ? (
              <>
                Your host runs scheduled backups between{" "}
                <strong>
                  {view?.window_start}–{view?.window_end} ({tz})
                </strong>
                . You choose what to back up, where, and how often — your host sets the window they run in.{" "}
              </>
            ) : (
              <>
                Scheduled backups run on the interval you choose and keep the same times of day ({tz}). You pick what
                to back up, where, and how often.{" "}
              </>
            )}
            <Tag>
              {view?.schedules.length ?? 0} / {view?.max_backup_schedules ?? 1}
            </Tag>
          </Typography.Paragraph>
          <Table<ScheduleRow>
            size="small"
            rowKey="id"
            columns={columns}
            dataSource={view?.schedules ?? []}
            pagination={false}
            locale={{ emptyText: "No backup schedules yet — add one to run backups automatically." }}
          />
        </Space>
      )}

      <Drawer
        title={editingId ? "Edit backup schedule" : "New backup schedule"}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        width={420}
        destroyOnClose
        extra={
          <Button type="primary" loading={saving} onClick={submitDrawer}>
            Save
          </Button>
        }
      >
        <Form<DrawerForm> form={form} layout="vertical">
          <Form.Item label="What to back up" name="content" rules={[{ required: true }]}>
            <Select options={CONTENT_OPTIONS} />
          </Form.Item>
          <Form.Item label="How often" name="cadence" rules={[{ required: true }]}>
            <Select options={CADENCE_OPTIONS} />
          </Form.Item>
          <Form.Item
            label="Destination"
            name="destination_id"
            tooltip="Required to enable a schedule."
          >
            <Select allowClear placeholder="Pick a destination" options={destOptions} notFoundContent="No allowed destinations" />
          </Form.Item>
          <Form.Item
            label="Days to keep"
            name="keep_within_days"
            tooltip={`Keeps every backup made in that many days, counted back from the newest. Your plan holds ${maxBackups} backups, so at this frequency up to ${keepDaysMax} day${keepDaysMax === 1 ? "" : "s"}.`}
            extra={keepDaysHint}
            rules={[
              {
                type: "number",
                max: keepDaysMax,
                message: `At this frequency your plan holds ${keepDaysMax} day${keepDaysMax === 1 ? "" : "s"} of backups`,
              },
            ]}
          >
            <InputNumber min={1} max={keepDaysMax} style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item label="Enabled" name="enabled" valuePropName="checked">
            <Switch checkedChildren="On" unCheckedChildren="Off" />
          </Form.Item>
        </Form>
      </Drawer>
    </Card>
  );
};
