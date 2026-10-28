// M30.1 Schedules admin tab. Lists every backup_schedules row, drawer
// for create/edit, multi-select destinations, "Run now" button.
import { useTranslation } from "react-i18next";
import { Alert, Button, Checkbox, Drawer, Form, InputNumber, Radio, Segmented, Select, Space, Switch, Table, Tag, TimePicker, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { RowActions } from "../../../components/RowActions";
import dayjs from "dayjs";
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
import { Timestamp } from "../../../components/Timestamp";
import {
  BACKUP_CADENCE_OPTIONS,
  BACKUP_CONTENT_OPTIONS,
  backupCadenceLabel,
  backupContentLabel,
} from "../../../lib/backupScheduleOptions";
import { HOURLY_EVERY, cronFromSpec, specFromCron, type Freq } from "./scheduleCron";

interface BackupSchedule {
  id: string;
  kind: "account_backup" | "system_backup";
  // Set only on an account's own schedule (made on its Backups page, or a
  // single-user schedule from before user_ids). Admin schedules leave it
  // empty and list their users in user_ids.
  user_id?: string | null;
  user_ids?: string[];
  include_system_backup?: boolean;
  cron_expr: string;
  // What each run backs up: full / files / database / folders.
  content?: string;
  // Set on an account's own schedule, which runs on this interval instead of
  // cron_expr (GH #2246). "" = a cron schedule.
  cadence?: string;
  enabled: boolean;
  keep_daily?: number | null;
  keep_weekly?: number | null;
  keep_monthly?: number | null;
  // An account's "Days to keep": every backup of the last N days (GH #2300).
  keep_within_days?: number | null;
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

// Scope is the schedule's backup TYPE, mirroring the on-demand
// "Create backup" drawer (GH #502). It maps onto the wire fields the
// backend already understands (kind + user_ids + include_system_backup):
//   account_backup — per-account fan-out ("All users" or selected accounts),
//                     optionally + a system backup via the include switch.
//   system_backup  — panel/system only, no account fan-out.
//   full_server    — every account PLUS the system, in one schedule
//                    (kind=account_backup, all users, include_system=true).
//   account_own    — one account's own schedule, the kind it adds on its
//                    Backups page: an interval instead of cron, one
//                    destination, under the account's plan (GH #2246).
type Scope = "account_backup" | "system_backup" | "full_server" | "account_own";

// scopeOf derives the drawer's Scope from an existing schedule row so
// editing reopens on the right type.
const scopeOf = (s: BackupSchedule): Scope => {
  if (s.kind === "system_backup") return "system_backup";
  const hasSelected = (s.user_ids ?? []).length > 0;
  if (!hasSelected && s.include_system_backup) return "full_server";
  return "account_backup";
};

// listedAsOwn: a row shown as an account's own schedule. A single-user admin
// schedule from before user_ids also has user_id set; one later widened to
// more users is listed with all of them, so none is hidden.
const listedAsOwn = (s: BackupSchedule) => !!s.user_id && (s.user_ids ?? []).length <= 1;

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

// An account's own schedule keeps days, as on its Backups page (GH #2300).
const KEEP_DAYS_HELP =
  "Every backup made in that many days is kept, counted back from the newest. It must fit in the account's plan limit at this frequency.";

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
  const enabled = Form.useWatch("enabled", form) as boolean | undefined;

  // GH #2246: an account's own schedule, set on its Backups page, runs on an
  // interval (cadence) instead of cron. It opens in the form the account edits
  // it in, with the same choices, instead of falling back to "Daily 03:00"
  // and sending that cron time on Save.
  // New schedules pick it as the Type "Account's own".
  const interval = editing ? !!editing.cadence : scope === "account_own";
  // An account's own schedule backs up that account only, so its account is
  // shown, not picked, and it can't become a System or Full Server schedule.
  const ownerID = editing?.user_id || "";
  const owner = users.find((u) => u.id === ownerID);
  const ownerLabel = owner ? `${owner.username} (${owner.email})` : ownerID;

  // A schedule's kind can't change once it exists (the API keeps the stored
  // one), and picking System here used to save an account schedule with an
  // empty user list, which backs up every account.
  const kindLocked = (value: Scope) => {
    if (!editing) return false;
    // A cron schedule can't become an interval one.
    if (value === "account_own") return true;
    if (ownerID) return value !== "account_backup";
    return editing.kind === "system_backup" ? value !== "system_backup" : value === "system_backup";
  };

  // A time the form can't show (set with the CLI, say) would be replaced by
  // the form's default on Save, the same failure as GH #2246; say so.
  const unreadCron = !!editing && !editing.cadence && specFromCron(editing.cron_expr) === null;

  useEffect(() => {
    if (open) {
      form.resetFields();
      if (editing?.cadence) {
        form.setFieldsValue({
          content: editing.content || "full",
          cadence: editing.cadence,
          destination_id: editing.destinations?.[0]?.id,
          keep: editing.keep_within_days ?? editing.keep_daily ?? undefined,
          enabled: editing.enabled,
        });
      } else if (editing) {
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
          every: spec.every ?? 1,
          minute: spec.freq === "hourly" ? spec.time.minute() : 0,
          weekdays: spec.weekdays ?? [1],
          dom: spec.dom ?? 1,
          enabled: editing.enabled,
          // An account's own schedule keeps days, like its Backups page.
          keep: editing.user_id
            ? (editing.keep_within_days ?? editing.keep_daily ?? undefined)
            : spec.freq === "daily" || spec.freq === "hourly"
              ? (editing.keep_daily ?? undefined)
              : spec.freq === "weekly"
                ? (editing.keep_weekly ?? undefined)
                : (editing.keep_monthly ?? undefined),
          include_system_backup: editing.include_system_backup ?? false,
          // An account's own schedule writes to one destination.
          destination_ids: editing.user_id
            ? editing.destinations?.[0]?.id
            : (editing.destinations?.map((d) => d.id) ?? []),
          content: editing.content || "full",
        });
      } else {
        form.setFieldsValue({
          scope: "account_backup" as Scope,
          content: "full",
          user_ids: [ALL_USERS],
          freq: "daily" as Freq,
          time: dayjs().hour(3).minute(0).second(0),
          every: 1,
          minute: 0,
          weekdays: [1],
          dom: 1,
          enabled: true,
          destination_ids: [],
          include_system_backup: false,
          cadence: "daily",
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
      if (interval) {
        // Only what the account can set itself. No cron time (the schedule
        // runs on its cadence), and no users or system backup (it backs up
        // its account only).
        const body = {
          content: values.content,
          cadence: values.cadence,
          destination_ids: values.destination_id ? [values.destination_id] : [],
          keep_within_days: values.keep ?? null,
          enabled: values.enabled,
        };
        if (editing) {
          await apiClient.patch(`/admin/backup-schedules/${editing.id}`, body);
          feedback.message.success("Schedule updated");
        } else {
          await apiClient.post("/admin/backup-schedules", {
            kind: "account_backup",
            user_ids: [values.owner_id],
            ...body,
          });
          feedback.message.success("Schedule created");
        }
        onSaved();
        return;
      }
      // Resolve the wire fields (kind / user_ids / include_system_backup)
      // from the chosen Scope (GH #502). Only the "account_backup" scope
      // reads the Users select; system_backup and full_server are
      // self-describing so their user list / system flag are fixed here.
      const scopeVal: Scope = values.scope ?? "account_backup";
      let kind = "account_backup";
      let userIDs: string[] = [];
      let includeSystem = false;
      if (ownerID) {
        // An account's own schedule keeps its account; neither is sent.
      } else if (scopeVal === "system_backup") {
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
          feedback.message.error("Pick at least one user, or 'All users'");
          setBusy(false);
          return;
        }
        includeSystem = !!values.include_system_backup;
      }
      if (values.freq === "weekly" && (!values.weekdays || values.weekdays.length === 0)) {
        feedback.message.error("Pick at least one weekday");
        setBusy(false);
        return;
      }
      const cron = cronFromSpec({
        freq: values.freq,
        // Hourly runs at a minute past the hour; the hour doesn't apply.
        time: values.freq === "hourly" ? dayjs().hour(0).minute(values.minute ?? 0) : values.time,
        every: values.every,
        weekdays: values.weekdays,
        dom: values.dom,
      });
      // One destination (an account's own schedule) or several.
      const dests: string[] = Array.isArray(values.destination_ids)
        ? values.destination_ids
        : values.destination_ids
          ? [values.destination_ids]
          : [];
      const body: Record<string, unknown> = {
        kind,
        cron_expr: cron,
        enabled: values.enabled,
        destination_ids: dests,
      };
      if (!ownerID) {
        body.user_ids = userIDs;
        body.include_system_backup = includeSystem;
      }
      if (kind === "account_backup") {
        body.content = values.content ?? "full";
      }
      // Retention is one knob, scoped to the chosen frequency. The
      // matching keep_* column is set; the other two stay null so
      // restic forget skips them.
      // Hourly keeps daily backups: retention has no hourly count.
      // An account's own schedule keeps days instead, as on its Backups page.
      if (ownerID) {
        body.keep_within_days = values.keep ?? null;
      } else {
        body.keep_daily = values.freq === "daily" || values.freq === "hourly" ? (values.keep ?? null) : null;
        body.keep_weekly = values.freq === "weekly" ? (values.keep ?? null) : null;
        body.keep_monthly = values.freq === "monthly" ? (values.keep ?? null) : null;
      }

      if (editing) {
        await apiClient.patch(`/admin/backup-schedules/${editing.id}`, body);
        feedback.message.success("Schedule updated");
      } else {
        await apiClient.post("/admin/backup-schedules", body);
        feedback.message.success("Schedule created");
      }
      onSaved();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Save failed"));
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
        {!(editing && interval) && (
          <Form.Item
            name="scope"
            label="Type"
            rules={[{ required: true }]}
            extra={
              scope === "system_backup"
                ? "Panel/system only — panel DBs, service config, mail state. No account files."
                : scope === "full_server"
                  ? "Every account (home, databases, mailboxes) PLUS the system, in one schedule. Disaster recovery."
                  : scope === "account_own"
                    ? "The schedule an account adds on its Backups page: one account, on an interval, within its plan's limits. The account sees it and can change it."
                    : "Per-account backup — all accounts or a chosen subset. Add the system with the switch below."
            }
          >
            <Radio.Group optionType="button" buttonStyle="solid">
              <Radio.Button value="account_backup" disabled={kindLocked("account_backup")}>Accounts</Radio.Button>
              <Radio.Button value="system_backup" disabled={kindLocked("system_backup")}>System</Radio.Button>
              <Radio.Button value="full_server" disabled={kindLocked("full_server")}>Full Server</Radio.Button>
              <Radio.Button value="account_own" disabled={kindLocked("account_own")}>Account&apos;s own</Radio.Button>
            </Radio.Group>
          </Form.Item>
        )}
        {interval ? (
          <>
            {editing ? (
              <>
                <Typography.Paragraph type="secondary">
                  This account set up this schedule on its Backups page. It backs up only this account and runs
                  on the interval below; these are the same choices the account has, and it sees your changes.
                </Typography.Paragraph>
                <Form.Item label="Account">
                  <Typography.Text>{ownerLabel}</Typography.Text>
                </Form.Item>
              </>
            ) : (
              <Form.Item
                name="owner_id"
                label="Account"
                rules={[{ required: true, message: "Pick an account" }]}
                extra="Its plan must allow scheduled backups and more than one schedule."
              >
                <Select
                  showSearch
                  placeholder="Pick an account"
                  optionFilterProp="label"
                  options={users
                    .filter((u) => !u.is_admin)
                    .map((u) => ({ value: u.id, label: `${u.username} (${u.email})` }))}
                />
              </Form.Item>
            )}
            <Form.Item name="content" label="What to back up" rules={[{ required: true }]}>
              <Select options={BACKUP_CONTENT_OPTIONS} />
            </Form.Item>
            <Form.Item
              name="cadence"
              label="How often"
              rules={[{ required: true }]}
              extra="When Settings restricts tenant backups to a maintenance window, it runs inside that window."
            >
              <Select options={BACKUP_CADENCE_OPTIONS} />
            </Form.Item>
            <Form.Item
              name="destination_id"
              label="Destination"
              rules={[{ required: !!enabled, message: "Pick a destination to enable this schedule" }]}
              extra="One the account's plan allows."
            >
              <Select
                allowClear
                placeholder="Pick a destination"
                options={destinations.map((d) => ({
                  value: d.id,
                  label: `${d.name} (${d.kind})${d.enabled ? "" : " — disabled"}`,
                  disabled: !d.enabled,
                }))}
              />
            </Form.Item>
            <Form.Item
              name="keep"
              label="Days to keep"
              extra={`${KEEP_DAYS_HELP}${editing ? " Leave blank to keep the current value." : ""}`}
            >
              <InputNumber min={1} style={{ width: 200 }} />
            </Form.Item>
          </>
        ) : (
          <>
            {unreadCron && (
              <Alert
                type="warning"
                showIcon
                style={{ marginBottom: 16 }}
                message={
                  <>
                    This form can&apos;t show this schedule&apos;s time (<code>{editing?.cron_expr}</code>). Saving
                    replaces it with the time below.
                  </>
                }
              />
            )}
            {scope === "account_backup" && !!ownerID && (
              <Form.Item label="Account" extra="This account's own schedule backs up only this account.">
                <Typography.Text>{ownerLabel}</Typography.Text>
              </Form.Item>
            )}
            {scope === "account_backup" && !ownerID && (
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
            {scope !== "system_backup" && (
              <Form.Item
                name="content"
                label="What to back up"
                rules={[{ required: true }]}
                extra={scope === "full_server" ? "For the account backups. The system backup is always complete." : undefined}
              >
                <Select options={BACKUP_CONTENT_OPTIONS} />
              </Form.Item>
            )}
            <Form.Item
              name="freq"
              label="Frequency"
              rules={[{ required: true }]}
            >
              <Segmented
                options={[
                  { label: "Hourly", value: "hourly" },
                  { label: "Daily", value: "daily" },
                  { label: "Weekly", value: "weekly" },
                  { label: "Monthly", value: "monthly" },
                ]}
              />
            </Form.Item>
            {freq === "hourly" && (
              <>
                <Form.Item
                  name="every"
                  label="Every"
                  rules={[{ required: true }]}
                  extra="Each run backs up every account this schedule covers."
                >
                  <Select
                    style={{ width: 200 }}
                    options={HOURLY_EVERY.map((n) => ({ value: n, label: n === 1 ? "Every hour" : `Every ${n} hours` }))}
                  />
                </Form.Item>
                <Form.Item name="minute" label="Minute past the hour" rules={[{ required: true, type: "number", min: 0, max: 59 }]}>
                  <InputNumber min={0} max={59} step={5} style={{ width: 120 }} />
                </Form.Item>
              </>
            )}
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
            {freq !== "hourly" && (
              <Form.Item
                name="time"
                label="Time"
                rules={[{ required: true, message: "Pick a time" }]}
              >
                <TimePicker format="HH:mm" minuteStep={5} />
              </Form.Item>
            )}
            <Form.Item
              name="destination_ids"
              label={ownerID ? "Destination" : "Destinations"}
              rules={[{ required: true, message: "Pick at least one destination" }]}
              extra={
                destinations.length === 0
                  ? "Create at least one destination first."
                  : "Each destination receives its own independent backup (no source-then-mirror)."
              }
            >
              <Select
                mode={ownerID ? undefined : "multiple"}
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
            {ownerID ? (
              <Form.Item name="keep" label="Days to keep" extra={KEEP_DAYS_HELP}>
                <InputNumber min={1} placeholder="keep all" style={{ width: 200 }} />
              </Form.Item>
            ) : (
              <Form.Item
                name="keep"
                extra={
                  freq === "hourly"
                    ? "Retention keeps one backup a day, so the daily sweep removes the day's other hourly backups."
                    : undefined
                }
                label={
                  freq === "daily" || freq === "hourly"
                    ? "Keep last N daily backups"
                    : freq === "weekly"
                      ? "Keep last N weekly backups"
                      : "Keep last N monthly backups"
                }
              >
                <InputNumber
                  min={0}
                  max={freq === "daily" || freq === "hourly" ? 365 : freq === "weekly" ? 104 : 120}
                  placeholder="keep all"
                  style={{ width: 200 }}
                />
              </Form.Item>
            )}
          </>
        )}
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
      feedback.message.error(extractApiError(err, "Load failed"));
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
          feedback.message.success("Schedule deleted");
          void reload();
        } catch (err) {
          feedback.message.error(extractApiError(err, "Delete failed"));
        }
      },
    });
  };

  const handleRunNow = async (row: BackupSchedule) => {
    try {
      await apiClient.post(`/admin/backup-schedules/${row.id}/run-now`, {});
      feedback.message.success("Schedule queued for the next tick (within 60s)");
      void reload();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Run-now failed"));
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
            // What the runs back up, the full account too, so every account
            // schedule says it (GH #2246).
            const content = <Tag>{backupContentLabel(row.content ?? "")}</Tag>;
            if (sc === "full_server") {
              return (
                <Space size={4} wrap>
                  <Tag color="purple">Full Server</Tag>
                  {content}
                </Space>
              );
            }
            return (
              <Space size={4} wrap>
                <Tag color="blue">
                  {listedAsOwn(row) ? "Account's own" : row.include_system_backup ? "Accounts + system" : "Accounts"}
                </Tag>
                {content}
              </Space>
            );
          }}
        />
        <Table.Column<BackupSchedule>
          title={t("schedulestab.users")}
          render={(_, row) => {
            if (row.kind === "system_backup") return "—";
            if (row.user_id && listedAsOwn(row)) {
              return <Tag>{users.find((u) => u.id === row.user_id)?.username ?? row.user_id.slice(0, 8)}</Tag>;
            }
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
          title={t("schedulestab.schedule")}
          render={(c: string, row: BackupSchedule) =>
            // An account's own schedule runs on its interval; it has no cron.
            row.cadence ? (
              backupCadenceLabel(row.cadence)
            ) : (
            <Tooltip
              title={
                row.next_firings && row.next_firings.length > 0
                  ? `Next firings:\n${row.next_firings.join("\n")}`
                  : ""
              }
            >
              <code>{c}</code>
            </Tooltip>
            )
          }
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
          render={(v: string | null) => <Timestamp value={v} />}
        />
        <Table.Column
          dataIndex="last_run_at"
          title={t("schedulestab.last_run")}
          render={(v: string | null) => <Timestamp value={v} />}
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
