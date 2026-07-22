// UserDatabaseDrawer — tenant Create-database Drawer (replaces the
// /jabali-panel/databases/create page route). Backend prepends the
// caller's username to the final database name.
import { useTranslation } from "react-i18next";
import { Button, Drawer, Form, Grid, Input, Segmented, Space, message } from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";
import { useCreateMutation } from "../../../hooks/useQueries";

type UserDatabaseCreateInput = { name: string; engine?: "mariadb" | "postgres" };
type DatabaseCreated = { id: string };

export interface UserDatabaseDrawerProps {
  open: boolean;
  onClose: () => void;
}

export const UserDatabaseDrawer = ({ open, onClose }: UserDatabaseDrawerProps) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<UserDatabaseCreateInput>();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);
  // M37 Phase 4: hide the PostgreSQL engine option when the operator
  // hasn't enabled it server-wide. Backend rejects engine=postgres
  // with 400 in that state — letting the user pick it would just
  // surface an error after Create.
  const [postgresEnabled, setPostgresEnabled] = useState(false);

  const createMutation = useCreateMutation<DatabaseCreated, UserDatabaseCreateInput>({
    resource: "databases",
  });

  useEffect(() => {
    if (!open) return;
    form.resetFields();
    apiClient
      .get<{ postgres_enabled: boolean }>("/me/server-capabilities")
      .then((r) => setPostgresEnabled(!!r.data.postgres_enabled))
      .catch(() => setPostgresEnabled(false));
  }, [open, form]);

  const handleFinish = async (values: UserDatabaseCreateInput) => {
    try {
      await createMutation.mutateAsync(values);
      message.success("Database created");
      onClose();
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Failed to create database");
    }
  };

  return (
    <Drawer
      title={t("userdatabasedrawer.create_database")}
      open={open}
      onClose={onClose}
      width={isDesktop ? 480 : undefined}
      placement="right"
      destroyOnClose
    >
      <Form<UserDatabaseCreateInput>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
        initialValues={{ engine: "mariadb" }}
      >
        {postgresEnabled && (
          <Form.Item
            label={t("userdatabasedrawer.engine")}
            name="engine"
            tooltip={t("userdatabasedrawer.mariadb_is_the_default_postgresql_must_be_en")}
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
          label={t("userdatabasedrawer.name")}
          name="name"
          rules={[
            { required: true, message: "Database name is required" },
            {
              pattern: /^[a-z][a-z0-9_]{0,30}$/,
              message:
                "Lowercase letters, digits and underscores only; must start with a letter; max 30 chars",
            },
          ]}
          tooltip={t("userdatabasedrawer.the_final_database_name_is_your_username_plu")}
          extra="Your username will be prepended automatically."
        >
          <Input placeholder="e.g. wp_prod" autoComplete="off" />
        </Form.Item>

        <Form.Item>
          <Space>
            <Button type="primary" htmlType="submit" loading={createMutation.isPending}>
              Create
            </Button>
            <Button onClick={onClose}>Cancel</Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
};
