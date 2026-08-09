// M30.1 Schedules admin tab. Lists every backup_schedules row, drawer
// for create/edit, multi-select destinations, "Run now" button.
import { useTranslation } from "react-i18next";
import { Alert, Button, Checkbox, Drawer, Form, InputNumber, Radio, Segmented, Select, Space, Switch, Table, Tag, TimePicker, Tooltip, Typography, message } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { RowActions } from "../../../components/RowActions";
import { shortDateTime } from "../../../utils/datetime";
import dayjs, { type Dayjs } from "dayjs";
import {
  CalendarCheckOutlined,
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  PlayCircleOutlined,
} from "@icons";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";

interface BackupSchedule {
  id: string;
  kind: "account_backup" | "system_backup";
  user_id?: string | null;
  user_ids?: string[];
  include_system_backup?: boolean;
  cron_expr: string;
  enabled: boolean;
  keep_daily?: number | null;
  keep_weekly?: number | null;
  keep_monthly?: number | null;
  last_run_at?: string | null;
  next_run_at?: string | null;
  next_firings?: string[];
  destinations?: Array<{ id: string; name: string; kind: string; enabled: boolean }>;
}

interface BackupDestinationOption {
  id: string;
  name: string;
  kind: string;
  enabled: boolean;
}

interface User {
  id: string;
  username: string;
  email: string;
  is_admin?: boolean;
}

// Sentinel value used for the "All non-admin users" multi-select tag.
// Wire-shape: empty user_ids[] = "every non-admin user" fan-out at
// tick time; non-empty = those specific users only. The submit handler
// strips ALL_USERS out of the array before sending.
const ALL_USERS = "__all__";

type Freq = "daily" | "weekly" | "monthly";

// Scope is the schedule's backup TYPE, mirroring the on-demand
// "Create backup" drawer (GH #502). It maps onto the wire fields the
// backend already understands (kind + user_ids + include_system_backup):
//   account_backup — per-account fan-out ("All users" or selected accounts),
//                     optionally + a system backup via the include switch.
//   system_backup  — panel/system only, no account fan-out.
//   full_server    — every account PLUS the system, in one schedule
//                    (kind=account_backup, all users, include_system=true).
type Scope = "account_backup" | "system_backup" | "full_server";

// scopeOf derives the drawer's Scope from an existing schedule row so
// editing reopens on the right type.
const scopeOf = (s: BackupSchedule): Scope => {
  if (s.kind === "system_backup") return "system_backup";
  const hasSelected = (s.user_ids ?? []).length > 0;
  if (!hasSelected && s.include_system_backup) return "full_server";
  return "account_backup";
};

interface ScheduleSpec {
  freq: Freq;
  time: Dayjs;          // hour:minute only used
  weekdays?: number[];  // weekly only — 0=Sun..6=Sat (cron numbering)
  dom?: number;         // monthly only — 1..31
}

// cronFromSpec converts the form values into a 5-field cron expression.
// Monthly is day-of-month only (Option A); nth-weekday lands when we
// switch off robfig/cron, which OR-merges dom/dow restrictions.
const cronFromSpec = (s: ScheduleSpec): string => {
  const m = s.time.minute();
  const h = s.time.hour();
  switch (s.freq) {
    case "daily":
      return `${m} ${h} * * *`;
    case "weekly": {
      const days = (s.weekdays ?? []).slice().sort((a, b) => a - b);
      const dow = days.length === 0 || days.length === 7 ? "*" : days.join(",");
      return `${m} ${h} * * ${dow}`;
    }
    case "monthly":
      return `${m} ${h} ${s.dom ?? 1} * *`;
  }
};

// specFromCron is best-effort — recognises the shapes cronFromSpec
// emits. Anything else returns null and the drawer falls back to
// daily-default with a notice.
const specFromCron = (expr: string): ScheduleSpec | null => {
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return null;
  const [mm, hh, dom, mon, dow] = parts;
  const m = Number(mm);
  const h = Number(hh);
  if (!Number.isInteger(m) || !Number.isInteger(h)) return null;
  if (mon !== "*") return null;
  const time = dayjs().hour(h).minute(m).second(0);
  // daily: dom * dow *
  if (dom === "*" && dow === "*") {
    return { freq: "daily", time };
  }
  // weekly: dom * dow restricted
  if (dom === "*") {
    const days = dow.split(",").map((x) => Number(x)).filter((n) => Number.isInteger(n));
    if (days.length === 0 || days.some((d) => d < 0 || d > 6)) return null;
    return { freq: "weekly", time, weekdays: days };
  }
  // monthly: dom restricted, dow *
  if (dow === "*") {
    const d = Number(dom);
    if (!Number.isInteger(d) || d < 1 || d > 31) return null;
    return { freq: "monthly", time, dom: d };
  }
  return null;
};

const WEEKDAY_OPTIONS = [
  { label: "Mon", value: 1 },
  { label: "Tue", value: 2 },
  { label: "Wed", value: 3 },
  { label: "Thu", value: 4 },
  { label: "Fri", value: 5 },
  { label: "Sat", value: 6 },
  { label: "Sun", value: 0 },
];

interface ScheduleDrawerProps {
  open: boolean;
  editing: BackupSchedule | null;
  destinations: BackupDestinationOption[];
  users: User[];
  onClose: () => void;
  onSaved: () => void;
}

function ScheduleDrawer({
  open,
  editing,
  destinations,
  users,
  onClose,
  onSaved,
}: ScheduleDrawerProps) {
  const [form] = Form.useForm();
  const [busy, setBusy] = useState(false);
  const freq = (Form.useWatch("freq", form) ?? "daily") as Freq;
  const scope = (Form.useWatch("scope", form) ?? "account_backup") as Scope;

  useEffect(() => {
    if (open) {
      form.resetFields();
      if (editing) {
        const spec = specFromCron(editing.cron_expr) ?? {
          freq: "daily" as Freq,
          time: dayjs().hour(3).minute(0).second(0),
        };
        const editingIDs = editing.user_ids && editing.user_ids.length > 0
          ? editing.user_ids
          : [ALL_USERS];
        form.setFieldsValue({
          scope: scopeOf(editing),
          user_ids: editingIDs,
          freq: spec.freq,
          time: spec.time,
          weekdays: spec.weekdays ?? [1],
          dom: spec.dom ?? 1,
          enabled: editing.enabled,
          keep:
            spec.freq === "daily"
              ? (editing.keep_daily ?? undefined)
              : spec.freq === "weekly"
                ? (editing.keep_weekly ?? undefined)
                : (editing.keep_monthly ?? undefined),
          include_system_backup: editing.include_system_backup ?? false,
          destination_ids: editing.destinations?.map((d) => d.id) ?? [],
        });
      } else {
        form.setFieldsValue({
          scope: "account_backup" as Scope,
          user_ids: [ALL_USERS],
          freq: "daily" as Freq,
          time: dayjs().hour(3).minute(0).second(0),
          weekdays: [1],
          dom: 1,
          enabled: true,
          destination_ids: [],
          include_system_backup: false,
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
      // Resolve the wire fields (kind / user_ids / include_system_backup)
      // from the chosen Scope (GH #502). Only the "account_backup" scope
      // reads the Users select; system_backup and full_server are
      // self-describing so their user list / system flag are fixed here.
      const scopeVal: Scope = values.scope ?? "account_backup";
      let kind = "account_backup";
      let userIDs: string[] = [];
      let includeSystem = false;
      if (scopeVal === "system_backup") {
        kind = "system_backup"; // panel/system only, no account fan-out
      } else if (scopeVal === "full_server") {
        // Every non-admin account + the system, in one schedule.
        userIDs = []; // empty = "all non-admin users" at tick time
        includeSystem = true;
      } else {
        const selected: string[] = values.user_ids ?? [];
        const hasAll = selected.includes(ALL_USERS);
        // ALL_USERS sentinel collapses to an empty list, which the
        // backend interprets as "every non-admin user" at tick time.
        // Any explicit picks while ALL_USERS is also selected are
        // dropped — operator either backs up everyone or a specific
        // subset, not both.
        userIDs = hasAll ? [] : selected.filter((u) => u !== ALL_USERS);
        if (!hasAll && userIDs.length === 0) {
          message.error("Pick at least one user, or 'All users'");
          setBusy(false);
          return;
        }
        includeSystem = !!values.include_system_backup;
      }
      if (values.freq === "weekly" && (!values.weekdays || values.weekdays.length === 0)) {
        message.error("Pick at least one weekday");
        setBusy(false);
        return;
      }
      const cron = cronFromSpec({
        freq: values.freq,
        time: values.time,
        weekdays: values.weekdays,
        dom: values.dom,
      });
      const body: Record<string, unknown> = {
        kind,
        cron_expr: cron,
        enabled: values.enabled,
        destination_ids: values.destination_ids ?? [],
        user_ids: userIDs,
        include_system_backup: includeSystem,
      };
      // Retention is one knob, scoped to the chosen frequency. The
      // matching keep_* column is set; the other two stay null so
      // restic forget skips them.
      body.keep_daily = values.freq === "daily" ? (values.keep ?? null) : null;
      body.keep_weekly = values.freq === "weekly" ? (values.keep ?? null) : null;
      body.keep_monthly = values.freq === "monthly" ? (values.keep ?? null) : null;

      if (editing) {
        await apiClient.patch(`/admin/backup-schedules/${editing.id}`, body);
        message.success("Schedule updated");
      } else {
        await apiClient.post("/admin/backup-schedules", body);
        message.success("Schedule created");
      }
      onSaved();
    } catch (err) {
      message.error(extractApiError(err, "Save failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Drawer
      title={editing ? "Edit schedule" : "New schedule"}
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
        <Form.Item
          name="scope"
          label="Type"
          rules={[{ required: true }]}
          extra={
            scope === "system_backup"
              ? "Panel/system only — panel DBs, service config, mail state. No account files."
              : scope === "full_server"
                ? "Every account (home, databases, mailboxes) PLUS the system, in one schedule. Disaster recovery."
                : "Per-account backup — all accounts or a chosen subset. Add the system with the switch below."
          }
        >
          <Radio.Group optionType="button" buttonStyle="solid">
            <Radio.Button value="account_backup">Accounts</Radio.Button>
            <Radio.Button value="system_backup">System</Radio.Button>
            <Radio.Button value="full_server">Full Server</Radio.Button>
          </Radio.Group>
        </Form.Item>
        {scope === "account_backup" && (
          <>
            <Form.Item
              name="user_ids"
              label="Users"
              rules={[{ required: true, message: "Pick at least one user, or 'All users'" }]}
              extra="Select 'All users' OR pick specific accounts. Admins are excluded from the list."
            >
              <Select
                mode="multiple"
                showSearch
                allowClear
                placeholder="Pick users (or 'All users')"
                optionFilterProp="label"
                options={[
                  { value: ALL_USERS, label: "All users (every non-admin)" },
                  ...users
                    .filter((u) => !u.is_admin)
                    .map((u) => ({
                      value: u.id,
                      label: `${u.username} (${u.email})`,
                    })),
                ]}
              />
            </Form.Item>
            <Form.Item
              name="include_system_backup"
              label="Include system backup"
              valuePropName="checked"
              extra="Also fire a system backup (panel DBs + service config + mail state + …) every time this schedule runs."
            >
              <Switch />
            </Form.Item>
          </>
        )}
        <Form.Item
          name="freq"
          label="Frequency"
          rules={[{ required: true }]}
        >
          <Segmented
            options={[
              { label: "Daily", value: "daily" },
              { label: "Weekly", value: "weekly" },
              { label: "Monthly", value: "monthly" },
            ]}
          />
        </Form.Item>
        {freq === "weekly" && (
          <Form.Item
            name="weekdays"
            label="Days"
            rules={[{ required: true, message: "Pick at least one weekday" }]}
          >
            <Checkbox.Group options={WEEKDAY_OPTIONS} />
          </Form.Item>
        )}
        {freq === "monthly" && (
          <Form.Item
            name="dom"
            label="Day of month"
            rules={[{ required: true, type: "number", min: 1, max: 31 }]}
            extra="1–31. Months without that day are skipped (e.g. day 31 in February)."
          >
            <InputNumber min={1} max={31} style={{ width: 120 }} />
          </Form.Item>
        )}
        <Form.Item
          name="time"
          label="Time"
          rules={[{ required: true, message: "Pick a time" }]}
        >
          <TimePicker format="HH:mm" minuteStep={5} />
        </Form.Item>
        <Form.Item
          name="destination_ids"
          label="Destinations"
          rules={[{ required: true, message: "Pick at least one destination" }]}
          extra={
            destinations.length === 0
              ? "Create at least one destination first."
              : "Each destination receives its own independent backup (no source-then-mirror)."
          }
        >
          <Select
            mode="multiple"
            placeholder="Select destinations"
            options={destinations.map((d) => ({
              value: d.id,
              label: `${d.name} (${d.kind})${d.enabled ? "" : " — disabled"}`,
              disabled: !d.enabled,
            }))}
          />
        </Form.Item>

        <Typography.Title level={5}>Retention</Typography.Title>
        <Typography.Paragraph type="secondary" style={{ marginTop: -8 }}>
          restic forget runs daily and prunes snapshots from this
          schedule beyond this limit. Leave blank to keep every snapshot.
        </Typography.Paragraph>
        <Form.Item
          name="keep"
          label={
            freq === "daily"
              ? "Keep last N daily backups"
              : freq === "weekly"
                ? "Keep last N weekly backups"
                : "Keep last N monthly backups"
          }
        >
          <InputNumber
            min={0}
            max={freq === "daily" ? 365 : freq === "weekly" ? 104 : 120}
            placeholder="keep all"
            style={{ width: 200 }}
          />
        </Form.Item>

        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
          <Switch />
        </Form.Item>
      </Form>
    </Drawer>
  );
}

export function SchedulesTab() {
  const { t } = useTranslation();
  const [rows, setRows] = useState<BackupSchedule[]>([]);
  const [destinations, setDestinations] = useState<BackupDestinationOption[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [loading, setLoading] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<BackupSchedule | null>(null);

  const reload = async () => {
    setLoading(true);
    try {
      const [s, d, u] = await Promise.all([
        apiClient.get<{ data: BackupSchedule[] }>("/admin/backup-schedules"),
        apiClient.get<{ data: BackupDestinationOption[] }>(
          "/admin/backup-destinations",
        ),
        apiClient.get<{ data: User[] }>("/users?page_size=500"),
      ]);
      setRows(s.data.data ?? []);
      setDestinations(d.data.data ?? []);
      setUsers(u.data.data ?? []);
    } catch (err) {
      message.error(extractApiError(err, "Load failed"));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void reload();
  }, []);

  const handleDelete = async (row: BackupSchedule) => {
    feedback.modal.confirm({
      title: `Delete schedule?`,
      content: `Backups already produced by this schedule remain. Future runs will not fire.`,
      okType: "danger",
      onOk: async () => {
        try {
          await apiClient.delete(`/admin/backup-schedules/${row.id}`);
          message.success("Schedule deleted");
          void reload();
        } catch (err) {
          message.error(extractApiError(err, "Delete failed"));
        }
      },
    });
  };

  const handleRunNow = async (row: BackupSchedule) => {
    try {
      await apiClient.post(`/admin/backup-schedules/${row.id}/run-now`, {});
      message.success("Schedule queued for the next tick (within 60s)");
      void reload();
    } catch (err) {
      message.error(extractApiError(err, "Run-now failed"));
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
          disabled={destinations.length === 0}
        >
          New schedule
        </Button>
      </Space>
      {destinations.length === 0 && (
        <Alert
          showIcon
          type="warning"
          style={{ marginBottom: 12 }}
          message={t("schedulestab.create_at_least_one_destination_before_addin")}
        />
      )}
      <Table<BackupSchedule>
        rowKey="id"
        loading={loading}
        dataSource={rows}
        pagination={false}
        scroll={{ x: "max-content" }}
      >
        <Table.Column<BackupSchedule>
          title="Type"
          render={(_, row) => {
            const sc = scopeOf(row);
            if (sc === "system_backup") return <Tag color="purple">System</Tag>;
            if (sc === "full_server") return <Tag color="purple">Full Server</Tag>;
            return (
              <Tag color="blue">
                {row.include_system_backup ? "Accounts + system" : "Accounts"}
              </Tag>
            );
          }}
        />
        <Table.Column<BackupSchedule>
          title={t("schedulestab.users")}
          render={(_, row) => {
            if (row.kind === "system_backup") return "—";
            const ids = row.user_ids ?? [];
            if (ids.length === 0) return <Tag color="blue">all users</Tag>;
            const names = ids
              .map((uid) => users.find((u) => u.id === uid)?.username ?? uid.slice(0, 8))
              .join(", ");
            return (
              <Tooltip title={names}>
                <Tag>{ids.length} selected</Tag>
              </Tooltip>
            );
          }}
        />
        <Table.Column
          dataIndex="cron_expr"
          title={t("schedulestab.cron")}
          render={(c: string, row: BackupSchedule) => (
            <Tooltip
              title={
                row.next_firings && row.next_firings.length > 0
                  ? `Next firings:\n${row.next_firings.join("\n")}`
                  : ""
              }
            >
              <code>{c}</code>
            </Tooltip>
          )}
        />
        <Table.Column
          dataIndex="enabled"
          title={t("schedulestab.enabled")}
          render={(v: boolean) => (v ? <Tag color="green">yes</Tag> : <Tag>no</Tag>)}
        />
        <Table.Column<BackupSchedule>
          title={t("schedulestab.destinations")}
          render={(_, row) => (
            <Space wrap>
              {(row.destinations ?? []).map((d) => (
                <Tag key={d.id}>
                  {d.name} ({d.kind})
                </Tag>
              ))}
              {(row.destinations ?? []).length === 0 && (
                <Typography.Text type="secondary">local-only</Typography.Text>
              )}
            </Space>
          )}
        />
        <Table.Column
          dataIndex="next_run_at"
          title={t("schedulestab.next_run")}
          render={(v: string | null) => v ?? "—"}
        />
        <Table.Column
          dataIndex="last_run_at"
          title={t("schedulestab.last_run")}
          render={(v: string | null) => shortDateTime(v)}
        />
        <Table.Column<BackupSchedule>
          title={t("schedulestab.actions")}
          render={(_, row) => (
            <RowActions
              actions={[
                { key: "run", label: "Run now", icon: <PlayCircleOutlined />, disabled: !row.enabled, onClick: () => handleRunNow(row) },
                { key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => { setEditing(row); setDrawerOpen(true); } },
                { key: "delete", label: "Delete", icon: <DeleteOutlined />, danger: true, onClick: () => handleDelete(row) },
              ]}
            />
          )}
        />
      </Table>
      <ScheduleDrawer
        open={drawerOpen}
        editing={editing}
        destinations={destinations}
        users={users}
        onClose={() => setDrawerOpen(false)}
        onSaved={() => {
          setDrawerOpen(false);
          void reload();
        }}
      />
      <Typography.Paragraph type="secondary" style={{ marginTop: 16 }}>
        <CalendarCheckOutlined /> Tick cadence: 60s. Local backup runs first; remote{" "}
        <code>restic copy</code> jobs are queued asynchronously after the local
        backup seals its manifest snapshot.
      </Typography.Paragraph>
    </>
  );
}
