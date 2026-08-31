// MigrateImapDrawer — GH #390 (Google Workspace) / #374 (M365) phase A.
//
// Per-mailbox IMAP migration: connect to a remote IMAP account with an
// app-password, preview its folder layout (POST /admin/migrations/imap/
// probe), then start the fetch+import (POST /admin/migrations/imap → 202
// + job id). The backend runs the pull in a detached goroutine; progress
// shows on the migration detail page (state pending → restoring → done).
//
// Distinct from CreateMigrationDrawer (cpmove/SSH panel-account sources):
// IMAP carries its own remote login per mailbox and never uploads a
// tarball. The app-password is sent per-request and never persisted.
import { useState } from "react";
import { Alert, Button, Collapse, Drawer, Form, Input, InputNumber, Space, Switch, Table, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { AxiosError } from "axios";

import { apiClient } from "../../../apiClient";
import { ImportOutlined } from "../../../icons";

type Props = { open: boolean; onClose: () => void };

type Folder = {
  name: string;
  role: string;
  selectable: boolean;
};

type ConnForm = {
  host: string;
  port?: number;
  starttls?: boolean;
  user: string;
  password: string;
  to: string;
  allow_private?: boolean;
};

// roleColor maps a SPECIAL-USE role to an antd Tag color for the preview.
const roleColor: Record<string, string> = {
  inbox: "blue",
  sent: "green",
  drafts: "gold",
  junk: "volcano",
  trash: "red",
  archive: "purple",
  all: "default",
};

function connBody(v: ConnForm) {
  return {
    host: v.host.trim(),
    port: v.port ?? 0,
    starttls: !!v.starttls,
    user: v.user.trim(),
    password: v.password,
    allow_private: !!v.allow_private,
  };
}

export function MigrateImapDrawer({ open, onClose }: Props) {
  const [form] = Form.useForm<ConnForm>();
  const qc = useQueryClient();
  const [folders, setFolders] = useState<Folder[] | null>(null);

  const probeMut = useMutation({
    mutationFn: async (v: ConnForm) => {
      const { data } = await apiClient.post<{ folders: Folder[]; total: number }>(
        "/admin/migrations/imap/probe",
        connBody(v),
      );
      return data.folders ?? [];
    },
    onSuccess: (f) => {
      setFolders(f);
      feedback.message.success(`Found ${f.length} folder(s) on the remote account`);
    },
    onError: (err: AxiosError<{ error?: string; detail?: string }>) => {
      // GH #1429: the API now returns a safe, actionable hint (wrong port /
      // STARTTLS mismatch / DNS / private host blocked / timeout / bad cert /
      // auth) in `detail`. Prefer it; fall back to the old generic strings.
      const hint = err.response?.data?.detail;
      if (hint) {
        feedback.message.error(hint);
      } else if (err.response?.status === 401) {
        feedback.message.error("The remote server rejected the credentials — check the app-password.");
      } else {
        feedback.message.error("Could not connect to the remote IMAP account.");
      }
    },
  });

  const startMut = useMutation({
    mutationFn: async (v: ConnForm) => {
      const { data } = await apiClient.post<{ job_id: string; state: string }>(
        "/admin/migrations/imap",
        { ...connBody(v), to: v.to.trim() },
      );
      return data;
    },
    onSuccess: async (d) => {
      feedback.message.success(`Migration started (job ${d.job_id}). Track progress in the list below.`);
      await qc.invalidateQueries({ queryKey: ["admin-migrations"] });
      handleClose();
    },
    onError: (err: AxiosError<{ error?: string; existing_job_id?: string }>) => {
      const body = err.response?.data;
      if (err.response?.status === 409) {
        feedback.message.error(`A migration is already running for this account (job ${body?.existing_job_id ?? "?"}).`);
      } else if (err.response?.status === 401) {
        feedback.message.error("The remote server rejected the credentials — check the app-password.");
      } else {
        feedback.message.error("Could not start the migration.");
      }
    },
  });

  function handleClose() {
    setFolders(null);
    form.resetFields();
    onClose();
  }

  async function onProbe() {
    // Probe only needs the connection fields, not the destination.
    // validateFields rejects on invalid input — swallow it (AntD paints
    // the field errors) so it doesn't surface as an unhandled rejection.
    try {
      const v = await form.validateFields(["host", "port", "starttls", "user", "password", "allow_private"]);
      probeMut.mutate(v as ConnForm);
    } catch {
      /* field-level errors are shown inline by the form */
    }
  }

  async function onStart() {
    try {
      const v = await form.validateFields();
      startMut.mutate(v);
    } catch {
      /* field-level errors are shown inline by the form */
    }
  }

  const columns = [
    { title: "Remote folder", dataIndex: "name", key: "name" },
    {
      title: "Role",
      dataIndex: "role",
      key: "role",
      render: (r: string) =>
        r ? <Tag color={roleColor[r] ?? "default"}>{r}</Tag> : <Typography.Text type="secondary">—</Typography.Text>,
    },
    {
      title: "Migrated",
      key: "migrated",
      render: (_: unknown, f: Folder) => {
        if (!f.selectable) return <Tag>container (skipped)</Tag>;
        if (f.role === "all") return <Tag color="default">skipped (duplicate of other folders)</Tag>;
        return <Tag color="green">yes</Tag>;
      },
    },
  ];

  return (
    <Drawer
      title={
        <Space>
          <ImportOutlined /> Migrate IMAP mailbox
        </Space>
      }
      width={620}
      open={open}
      onClose={handleClose}
      destroyOnClose
      extra={
        <Space>
          <Button onClick={handleClose}>Cancel</Button>
          <Button
            type="primary"
            loading={startMut.isPending}
            onClick={onStart}
          >
            Start migration
          </Button>
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="App-password required"
        description="Use a Google Workspace / Microsoft 365 app-password (not the account password). The remote mailbox is read-only during the pull; messages de-duplicate on Message-ID, so re-running resumes safely. The destination mailbox must already exist."
      />

      <Form form={form} layout="vertical" requiredMark="optional">
        <Form.Item
          name="host"
          label="Remote IMAP host"
          rules={[{ required: true, message: "e.g. imap.gmail.com or outlook.office365.com" }]}
        >
          <Input placeholder="imap.gmail.com" autoComplete="off" />
        </Form.Item>

        <Form.Item
          name="user"
          label="Remote login"
          rules={[{ required: true, message: "the remote email address" }]}
        >
          <Input placeholder="old-account@company.com" autoComplete="off" />
        </Form.Item>

        <Form.Item
          name="password"
          label="App-password"
          rules={[{ required: true, message: "app-password" }]}
        >
          <Input.Password placeholder="app-password" autoComplete="new-password" />
        </Form.Item>

        <Form.Item
          name="to"
          label="Destination mailbox"
          rules={[{ required: true, message: "the local jabali mailbox to import into" }]}
          extra="Must already exist on this server."
        >
          <Input placeholder="new-account@example.com" autoComplete="off" />
        </Form.Item>

        <Collapse
          ghost
          items={[
            {
              key: "adv",
              label: "Advanced",
              children: (
                <>
                  <Form.Item name="port" label="Port" extra="Leave blank for 993 (implicit TLS) / 143 (STARTTLS).">
                    <InputNumber min={1} max={65535} style={{ width: "100%" }} placeholder="993" />
                  </Form.Item>
                  <Form.Item name="starttls" label="Use STARTTLS (port 143)" valuePropName="checked">
                    <Switch />
                  </Form.Item>
                  <Form.Item
                    name="allow_private"
                    label="Allow private/LAN host"
                    valuePropName="checked"
                    extra="Only enable when migrating from a server on a private network."
                  >
                    <Switch />
                  </Form.Item>
                </>
              ),
            },
          ]}
        />

        <Button onClick={onProbe} loading={probeMut.isPending} style={{ marginBottom: 16 }}>
          Preview folders
        </Button>
      </Form>

      {folders && (
        <Table<Folder>
          size="small"
          rowKey="name"
          columns={columns}
          dataSource={folders}
          pagination={false}
        />
      )}
    </Drawer>
  );
}
