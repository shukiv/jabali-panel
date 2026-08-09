// CreateBackupDrawer — admin creates either a per-user account
// backup (kind=account_backup) or a system_backup. The agent
// orchestrator runs the actual stages; the panel just creates the
// workflow row + dispatches.
import { useTranslation } from "react-i18next";
import { Alert, Button, Drawer, Form, Grid, Input, Radio, Select, Space } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";
import { useListQuery } from "../../../hooks/useQueries";

type User = {
  id: string;
  username: string;
  email: string;
  is_admin: boolean;
};

type Kind = "account_backup" | "system_backup" | "full_server";

interface CreateBackupDrawerProps {
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}

interface FormValues {
  kind: Kind;
  user_id?: string;
  destination_id?: string;
  databases?: string;
  mailboxes?: string;
  content?: string;
  folders?: string;
  compression?: string;
}

type Destination = { id: string; name: string; kind: string; enabled: boolean };

export const CreateBackupDrawer = ({ open, onClose, onCreated }: CreateBackupDrawerProps) => {
  const { t } = useTranslation();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);
  const [form] = Form.useForm<FormValues>();
  const [submitting, setSubmitting] = useState(false);
  const kind = Form.useWatch("kind", form) ?? "account_backup";
  const content = Form.useWatch("content", form) ?? "full";

  const usersQuery = useListQuery<User>({
    resource: "users",
    params: { pageSize: 500 },
    enabled: open && kind === "account_backup",
  });
  const destQuery = useListQuery<Destination>({
    resource: "admin/backup-destinations",
    params: { pageSize: 100 },
    enabled: open,
  });

  useEffect(() => {
    if (!open) {
      form.resetFields();
    } else {
      form.setFieldValue("kind", "account_backup");
    }
  }, [open, form]);

  const handleSubmit = async (values: FormValues) => {
    setSubmitting(true);
    try {
      if (!values.destination_id) {
        feedback.message.error("Pick a destination");
        return;
      }
      if (values.kind === "system_backup" || values.kind === "full_server") {
        // Full Server = a system backup that also walks every account (#502).
        // The agent's system.backup already supports include_accounts, so this
        // is one job covering the system/panel + all accounts' files/DBs/mail.
        const full = values.kind === "full_server";
        await apiClient.post(`/admin/system/backups`, {
          include_accounts: full,
          destination_id: values.destination_id,
        });
        feedback.message.success(full ? "Full server backup queued" : "System backup queued");
        onCreated();
        return;
      }
      if (!values.user_id) {
        feedback.message.error("Pick a user");
        return;
      }
      const payload = {
        destination_id: values.destination_id,
        databases: values.databases
          ? values.databases.split(",").map((s) => s.trim()).filter(Boolean)
          : [],
        mailboxes: values.mailboxes
          ? values.mailboxes.split(",").map((s) => s.trim()).filter(Boolean)
          : [],
        content: values.content ?? "full",
        folders: values.folders
          ? values.folders.split(",").map((s) => s.trim()).filter(Boolean)
          : [],
        compression: values.compression ?? "",
      };
      await apiClient.post(`/admin/users/${values.user_id}/backups`, payload);
      feedback.message.success("Backup queued");
      onCreated();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Create failed"));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Drawer
      title={t("createbackupdrawer.create_backup")}
      open={open}
      onClose={onClose}
      width={isDesktop ? 520 : undefined}
      placement="right"
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="primary" loading={submitting} onClick={() => form.submit()}>
            Create backup
          </Button>
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        message={t("createbackupdrawer.backups_run_as_a_goroutine_inside_panel_agen")}
        description={
          kind === "full_server"
            ? "Full Server: the complete system/panel backup PLUS every account (each account's home, databases, and mailboxes) in one run. Best for disaster recovery."
            : kind === "system_backup"
              ? "Stages: panel_db (per system DB) → panel_config → service_config → mail_state → tls → security → os_users → data_state → manifest."
              : "Stages: home → databases → mailboxes → metadata → manifest. Each stage produces a separate restic snapshot tagged with the job-id."
        }
        style={{ marginBottom: 16 }}
      />
      <Form<FormValues>
        form={form}
        layout="vertical"
        onFinish={handleSubmit}
        initialValues={{ kind: "account_backup" }}
      >
        <Form.Item label={t("createbackupdrawer.type")} name="kind" rules={[{ required: true }]}>
          <Radio.Group>
            <Radio.Button value="account_backup">Account</Radio.Button>
            <Radio.Button value="system_backup">System</Radio.Button>
            <Radio.Button value="full_server">Full Server</Radio.Button>
          </Radio.Group>
        </Form.Item>

        <Form.Item
          label={t("createbackupdrawer.destination")}
          name="destination_id"
          rules={[{ required: true, message: "Pick a destination" }]}
          extra="The backup writes directly to this destination — no local source repo."
        >
          <Select
            placeholder={t("createbackupdrawer.pick_a_destination")}
            loading={destQuery.isLoading}
            options={(destQuery.items ?? [])
              .filter((d) => d.enabled)
              .map((d) => ({ value: d.id, label: `${d.name} (${d.kind})` }))}
          />
        </Form.Item>

        {kind === "account_backup" && (
          <>
            <Form.Item
              label={t("createbackupdrawer.user")}
              name="user_id"
              rules={[{ required: true, message: "Pick a user" }]}
            >
              <Select
                placeholder={t("createbackupdrawer.pick_a_user")}
                showSearch
                optionFilterProp="label"
                loading={usersQuery.isLoading}
                options={(usersQuery.items ?? [])
                  .filter((u) => !u.is_admin)
                  .map((u) => ({
                    value: u.id,
                    label: `${u.username} (${u.email})`,
                  }))}
              />
            </Form.Item>
            <Form.Item
              label={t("createbackupdrawer.databases_comma_separated_optional")}
              name="databases"
              extra="Names of databases owned by the user. Leave empty to skip the DB stage."
            >
              <Input placeholder="alice_wp, alice_blog" />
            </Form.Item>
            <Form.Item
              label={t("createbackupdrawer.mailboxes_comma_separated_optional")}
              name="mailboxes"
              extra="user@domain pairs. Skips with warning when Stalwart is down."
            >
              <Input placeholder="alice@example.com, hello@example.com" />
            </Form.Item>
            <Form.Item
              label={t("createbackupdrawer.content")}
              name="content"
              extra="Full = home + databases + mailboxes. Files = home only. Databases = DBs only. Folders = a subset of the home directory."
            >
              <Select
                options={[
                  { value: "full", label: "Full account" },
                  { value: "files", label: "Files only (home)" },
                  { value: "database", label: "Databases only" },
                  { value: "folders", label: "Specific folders" },
                ]}
              />
            </Form.Item>
            {content === "folders" && (
              <Form.Item
                label={t("createbackupdrawer.folders_comma_separated")}
                name="folders"
                rules={[{ required: true, message: "List at least one folder" }]}
                extra="Paths relative to the account home, e.g. public_html, public_html/wp-content/uploads"
              >
                <Input placeholder="public_html, mail" />
              </Form.Item>
            )}
            <Form.Item
              label={t("createbackupdrawer.compression")}
              name="compression"
              extra="restic compression level (zstd). Auto is recommended; Max is smaller but slower; Off is fastest."
            >
              <Select
                options={[
                  { value: "", label: "Auto (recommended)" },
                  { value: "max", label: "Max (smallest)" },
                  { value: "off", label: "Off (fastest)" },
                ]}
              />
            </Form.Item>
          </>
        )}

      </Form>
    </Drawer>
  );
};
