// Admin-side "Create database" form. Backend accepts only the bare
// name segment — non-admin users get their username auto-prefixed
// on the server (see panel-api/internal/api/databases.go). Admins
// create without a prefix. The helper text reflects that distinction.
import { useTranslation } from "react-i18next";
import { Button, Card, Form, Input, Typography, message } from "antd";
import { useNavigate } from "react-router";

import { useCreateMutation } from "../../../hooks/useQueries";

type DatabaseCreateInput = {
  name: string;
};

type DatabaseCreated = { id: string };

export const DatabaseCreate = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [form] = Form.useForm<DatabaseCreateInput>();
  const createMutation = useCreateMutation<DatabaseCreated, DatabaseCreateInput>({
    resource: "databases",
  });

  const handleFinish = async (values: DatabaseCreateInput) => {
    try {
      await createMutation.mutateAsync(values);
      message.success("Database created");
      navigate("/jabali-admin/databases");
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to create database";
      message.error(msg);
    }
  };

  return (
    <Card>
      <Typography.Title level={3} style={{ marginTop: 0 }}>
        Create database
      </Typography.Title>
      <Form<DatabaseCreateInput>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
      >
        <Form.Item
          label={t("databasecreate.name")}
          name="name"
          rules={[
            { required: true, message: "Database name is required" },
            {
              pattern: /^[a-z][a-z0-9_]{0,30}$/,
              message:
                "Lowercase letters, digits and underscores only; must start with a letter; max 30 chars",
            },
          ]}
          tooltip={t("databasecreate.admins_create_databases_without_a_username_p")}
        >
          <Input placeholder="e.g. wp_prod" autoComplete="off" />
        </Form.Item>

        <Form.Item>
          <Button
            type="primary"
            htmlType="submit"
            loading={createMutation.isPending}
          >
            Save
          </Button>
        </Form.Item>
      </Form>
    </Card>
  );
};
