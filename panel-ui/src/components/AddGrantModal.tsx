// AddGrantModal — per-user grant creation.
//
// Given a database user (by id + display username), lets the operator
// pick a database and either use a preset grant level (rw → "Full Access",
// ro → "Read only") or custom privileges. Supports granular privilege
// checkbox set (SELECT/INSERT/UPDATE/DELETE/CREATE/DROP/ALTER/INDEX).
import { useEffect, useState } from "react";
import { Alert, Form, Modal, Radio, Select, Checkbox, Space } from "antd";
import { feedback } from "../lib/feedback"; // GH #970: themed toasts

import { apiClient } from "../apiClient";
import { useListQuery } from "../hooks/useQueries";

interface AddGrantModalProps {
  open: boolean;
  userId: string | null;
  username: string;
  /** Database ids this user already has a grant on — pre-filtered in the picker. */
  excludedDatabaseIds: string[];
  /** User's engine — picker filters to same-engine databases only. Cross-engine
   *  grants (mariadb user → postgres db) crash on the agent side. */
  userEngine?: "mariadb" | "postgres";
  onClose: () => void;
  /** Called after a successful grant POST so the parent can refresh its table. */
  onSuccess: () => void;
}

type DatabaseOption = { id: string; name: string; engine?: "mariadb" | "postgres" };

const AVAILABLE_PRIVILEGES = ["SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "INDEX"];

type AddGrantInput = {
  database_id: string;
  grantType: "preset" | "custom";
  grant_level?: "rw" | "ro";
  privileges?: string[];
};

export function AddGrantModal({
  open,
  userId,
  username,
  excludedDatabaseIds,
  userEngine,
  onClose,
  onSuccess,
}: AddGrantModalProps) {
  const [form] = Form.useForm<AddGrantInput>();
  const [submitting, setSubmitting] = useState(false);
  const grantType = Form.useWatch("grantType", form);

  // Fresh list of databases each open — 200 is plenty for a
  // single-tenant panel; large installs can move to an async select.
  const { items: dbItems, isLoading } = useListQuery<DatabaseOption>({
    resource: "databases",
    params: { pageSize: 200 },
    enabled: open,
  });
  const databases = dbItems.filter(
    (d) =>
      !excludedDatabaseIds.includes(d.id) &&
      // Same-engine only: a mariadb user can't grant on a postgres db
      // (different role namespace, different agent command). Default
      // to mariadb when either side is missing engine — pre-M37 rows.
      (userEngine ?? "mariadb") === (d.engine ?? "mariadb"),
  );

  useEffect(() => {
    if (open) {
      form.resetFields();
      form.setFieldsValue({ grantType: "preset", grant_level: "rw", privileges: [] });
    }
  }, [open, form]);

  const onFinish = async (values: AddGrantInput) => {
    if (!userId) return;
    setSubmitting(true);
    try {
      // Build the request: send privileges array if custom, otherwise send grant_level
      const payload: Record<string, any> = {
        database_id: values.database_id,
      };

      if (values.grantType === "custom" && values.privileges && values.privileges.length > 0) {
        payload.privileges = values.privileges;
      } else {
        payload.grant_level = values.grant_level || "rw";
      }

      await apiClient.post(`/database-users/${userId}/grants`, payload);
      feedback.message.success("Access granted");
      onSuccess();
      onClose();
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ?? "Failed to grant access";
      feedback.message.error(msg);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      title="Add Database Access"
      open={open}
      onCancel={onClose}
      okText="Grant Access"
      okButtonProps={{ loading: submitting, onClick: () => form.submit() }}
      destroyOnClose
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        title={`Grant privileges to ${username || "—"}@localhost`}
      />

      <Form<AddGrantInput>
        form={form}
        layout="vertical"
        initialValues={{ grantType: "preset", grant_level: "rw", privileges: [] }}
        onFinish={onFinish}
      >
        <Form.Item
          label="Database"
          name="database_id"
          rules={[{ required: true, message: "Pick a database" }]}
        >
          <Select<string>
            loading={isLoading}
            showSearch
            optionFilterProp="label"
            placeholder="Select a database"
            options={databases.map((d) => ({ value: d.id, label: d.name }))}
            notFoundContent={
              excludedDatabaseIds.length > 0 && databases.length === 0
                ? "User already has access to every database."
                : undefined
            }
          />
        </Form.Item>

        <Form.Item label="Grant Type" name="grantType" rules={[{ required: true }]}>
          <Radio.Group>
            <Space direction="vertical" style={{ width: "100%" }}>
              <Radio value="preset">Preset Privileges</Radio>
              <Radio value="custom">Custom Privileges</Radio>
            </Space>
          </Radio.Group>
        </Form.Item>

        {grantType === "preset" && (
          <Form.Item label="Privilege Level" name="grant_level" rules={[{ required: true }]}>
            <Radio.Group>
              <Space direction="vertical">
                <Radio value="rw">Full Access (all privileges)</Radio>
                <Radio value="ro">Read Only (SELECT only)</Radio>
              </Space>
            </Radio.Group>
          </Form.Item>
        )}

        {grantType === "custom" && (
          <Form.Item label="Custom Privileges" name="privileges">
            <Checkbox.Group
              options={AVAILABLE_PRIVILEGES.map((p) => ({ label: p, value: p }))}
            />
          </Form.Item>
        )}
      </Form>
    </Modal>
  );
}
