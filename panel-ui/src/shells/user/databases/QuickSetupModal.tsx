// Quick Database Setup modal: creates a database + user + grants ALL
// privileges on that database in one click. Three sequential API calls:
//
//   1. POST /databases             -> {id, name, ...}
//   2. POST /database-users        -> {id, username, password}   // password is shown once
//   3. POST /database-users/:id/grants  {database_id, privileges: ["ALL"]}
//
// Rollback: if step 2 fails we leave the DB in place and surface it to
// the user (they can delete it); if step 3 fails we leave DB + user and
// surface the partial credentials so the user can grant manually.
// Atomic rollback would need a new backend endpoint — out of scope for
// the shortcut.

import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Modal, Form, Input, Button, Segmented, Space, Typography, Alert } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { CheckCircleTwoTone } from "@icons";
import { useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";
import { CopyableInput } from "../../../components/CopyableInput";

type Props = {
  open: boolean;
  onClose: () => void;
  onSuccess: () => void;
};

type CreatedResult = {
  databaseName: string;
  username: string;
  password: string;
};

type ApiError = {
  response?: { data?: { error?: string; detail?: string } };
  message?: string;
};

function extractError(err: unknown, fallback: string): string {
  const e = err as ApiError;
  return (
    e.response?.data?.detail ??
    e.response?.data?.error ??
    e.message ??
    fallback
  );
}

export const QuickSetupModal = ({ open, onClose, onSuccess }: Props) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<{ name: string; engine: "mariadb" | "postgres" }>();
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<CreatedResult | null>(null);
  // M37 Phase 4: hide engine picker if PostgreSQL not enabled server-wide.
  const [postgresEnabled, setPostgresEnabled] = useState(false);
  const qc = useQueryClient();

  useEffect(() => {
    if (!open) return;
    apiClient
      .get<{ postgres_enabled: boolean }>("/me/server-capabilities")
      .then((r) => setPostgresEnabled(!!r.data.postgres_enabled))
      .catch(() => setPostgresEnabled(false));
  }, [open]);
  // The DB + user tables are rendered by sibling Refine components with
  // their own query caches; bumping onSuccess() alone only refetched the
  // databases table. Invalidate the database-users cache too so the
  // newly-created user appears without a manual reload.
  const refreshLists = () => {
    qc.invalidateQueries({ queryKey: ["list", "databases"] });
    qc.invalidateQueries({ queryKey: ["list", "database-users"] });
    onSuccess();
  };

  const reset = () => {
    form.resetFields();
    setResult(null);
  };

  const handleClose = () => {
    reset();
    onClose();
  };

  const handleSubmit = async () => {
    try {
      await form.validateFields();
    } catch {
      return;
    }
    const { name, engine = "mariadb" } = form.getFieldsValue();
    setSubmitting(true);
    try {
      const dbResp = await apiClient.post<{ id: string; name: string }>(
        "/databases",
        { name, engine },
      );
      const dbId = dbResp.data.id;
      const dbName = dbResp.data.name;

      let userId: string;
      let username: string;
      let password: string;
      try {
        const userResp = await apiClient.post<{
          id: string;
          username: string;
          password: string;
        }>("/database-users", { username: name, engine });
        userId = userResp.data.id;
        username = userResp.data.username;
        password = userResp.data.password;
      } catch (err) {
        feedback.message.error(
          `Database "${dbName}" was created, but user creation failed: ${extractError(err, "unknown")}. You can delete the database from the list.`,
        );
        refreshLists();
        return;
      }

      try {
        await apiClient.post(`/database-users/${userId}/grants`, {
          database_id: dbId,
          privileges: ["ALL"],
        });
      } catch (err) {
        feedback.message.warning(
          `Database "${dbName}" and user "${username}" were created, but the grant failed: ${extractError(err, "unknown")}. You can add the grant manually.`,
        );
        setResult({ databaseName: dbName, username, password });
        refreshLists();
        return;
      }

      setResult({ databaseName: dbName, username, password });
      refreshLists();
    } catch (err) {
      feedback.message.error(extractError(err, "Failed to create database"));
    } finally {
      setSubmitting(false);
    }
  };


  return (
    <Modal
      title={t("quicksetupmodal.quick_database_setup")}
      open={open}
      onCancel={handleClose}
      maskClosable={!submitting && !result}
      footer={
        result
          ? [
              <Button key="done" type="primary" onClick={handleClose}>
                Done
              </Button>,
            ]
          : [
              <Button key="cancel" onClick={handleClose} disabled={submitting}>
                Cancel
              </Button>,
              <Button
                key="submit"
                type="primary"
                loading={submitting}
                onClick={handleSubmit}
              >
                Create Database & User
              </Button>,
            ]
      }
      destroyOnClose
    >
      {!result && (
        <>
          <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
            Create a database and user with full access in one step.
          </Typography.Paragraph>
          <Form
            form={form}
            layout="vertical"
            disabled={submitting}
            initialValues={{ engine: "mariadb" }}
          >
            {postgresEnabled && (
              <Form.Item
                label={t("quicksetupmodal.engine")}
                name="engine"
                tooltip={t("quicksetupmodal.mariadb_is_the_default_postgresql_must_be_en")}
              >
                <Segmented
                  options={[
                    { label: "MariaDB", value: "mariadb" },
                    { label: "PostgreSQL", value: "postgres" },
                  ]}
                />
              </Form.Item>
            )}
            <Form.Item
              label={t("quicksetupmodal.database_user_name")}
              name="name"
              rules={[
                { required: true, message: "Name is required" },
                {
                  pattern: /^[a-z][a-z0-9_]{0,30}$/,
                  message:
                    "Lowercase letters, digits, and underscores; must start with a letter; max 30 chars",
                },
              ]}
              extra="Your username prefix is added automatically (e.g. alice_wp)."
            >
              <Input placeholder="e.g. wp_prod" autoComplete="off" />
            </Form.Item>
          </Form>
        </>
      )}

      {result && (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Alert
            type="success"
            showIcon
            icon={<CheckCircleTwoTone twoToneColor="#52c41a" />}
            title={t("quicksetupmodal.database_and_user_created")}
            description={t("quicksetupmodal.copy_the_password_now_it_is_shown_only_once")}
          />
          <div>
            <Typography.Text strong>Database</Typography.Text>
            <CopyableInput value={result.databaseName} />
          </div>
          <div>
            <Typography.Text strong>Username</Typography.Text>
            <CopyableInput value={result.username} />
          </div>
          <div>
            <Typography.Text strong>Password</Typography.Text>
            <CopyableInput value={result.password} secret />
          </div>
        </Space>
      )}
    </Modal>
  );
};
