// Python app environment editor (Gitea #549). Opens from each app's row,
// loads the current env via GET /python-apps/:id, and saves through the
// existing PUT /python-apps/:id/env contract (full-set replace). Values are
// masked by default (deliberate per-field reveal) and the drawer states that a
// restart is required for changes to take effect. A Node.js app (ADR-0175)
// cannot set PORT or HOST: the panel sets them to the app's loopback port.
import { Alert, App, Button, Drawer, Form, Input, Space, Typography } from "antd";
import { DeleteOutlined, PlusOutlined } from "@icons";
import { useEffect, useState } from "react";

import { StandardDrawerFooter } from "../../../components/StandardActionFooter";
import { extractApiError } from "../../../apiErrors";
import {
  fetchPythonAppEnv,
  useUpdatePythonAppEnv,
  type PythonApp,
} from "./usePythonApps";

type EnvForm = { vars: { key: string; value: string }[] };

export function PythonAppEnvDrawer({
  app,
  onClose,
}: {
  app: PythonApp | null;
  onClose: () => void;
}) {
  const { message } = App.useApp();
  const [form] = Form.useForm<EnvForm>();
  const [loading, setLoading] = useState(false);
  const update = useUpdatePythonAppEnv();

  useEffect(() => {
    if (!app) return;
    setLoading(true);
    form.setFieldsValue({ vars: [] });
    fetchPythonAppEnv(app.id)
      .then((rows) =>
        form.setFieldsValue({
          vars: rows.length ? rows : [{ key: "", value: "" }],
        }),
      )
      .catch((e) => message.error(extractApiError(e) ?? "Failed to load environment"))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [app]);

  const save = async () => {
    const { vars } = await form.validateFields();
    const env: Record<string, string> = {};
    for (const v of vars ?? []) {
      const k = (v.key ?? "").trim();
      if (k) env[k] = v.value ?? "";
    }
    try {
      await update.mutateAsync({ id: app!.id, env });
      message.success("Environment saved — restart the app to apply.");
      onClose();
    } catch (e) {
      message.error(extractApiError(e) ?? "Failed to save environment");
    }
  };

  return (
    <Drawer
      title={app ? `Environment — ${app.name}` : "Environment"}
      open={app !== null}
      onClose={onClose}
      width={560}
      footer={
        <StandardDrawerFooter
          primaryText="Save"
          primaryLoading={update.isPending}
          onPrimary={() => void save()}
          onCancel={onClose}
        />
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="A restart is required"
        description="Saving replaces the app's full environment. Restart the app (Restart action) for the new values to take effect."
      />
      {app?.runtime === "node" && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message="PORT and HOST are set by the panel"
          description="The app listens on its own loopback port, which the panel passes in PORT and HOST. NODE_ENV is production unless you set it here."
        />
      )}
      <Alert
        type="warning"
        showIcon
        style={{ marginBottom: 16 }}
        message="Treat these values as secrets"
        description="Values are masked below; reveal a field deliberately with its eye toggle. They are sent over the existing env API and stored on the server."
      />
      <Form form={form} layout="vertical" disabled={loading}>
        <Form.List name="vars">
          {(fields, { add, remove }) => (
            <>
              {fields.map((field) => (
                <Space
                  key={field.key}
                  align="baseline"
                  style={{ display: "flex", marginBottom: 8 }}
                >
                  <Form.Item
                    {...field}
                    name={[field.name, "key"]}
                    rules={[
                      {
                        pattern: /^[A-Za-z_][A-Za-z0-9_]*$/,
                        message: "KEY must be A-Z a-z 0-9 _ (not starting with a digit)",
                      },
                      {
                        validator: (_, v?: string) =>
                          app?.runtime === "node" && ["PORT", "HOST"].includes((v ?? "").trim())
                            ? Promise.reject(new Error("The panel sets PORT and HOST"))
                            : Promise.resolve(),
                      },
                    ]}
                    style={{ marginBottom: 0 }}
                  >
                    <Input placeholder="KEY" style={{ width: 200 }} />
                  </Form.Item>
                  <Form.Item
                    {...field}
                    name={[field.name, "value"]}
                    style={{ marginBottom: 0 }}
                  >
                    <Input.Password
                      placeholder="value"
                      style={{ width: 240 }}
                      visibilityToggle
                    />
                  </Form.Item>
                  <Button
                    type="text"
                    danger
                    icon={<DeleteOutlined />}
                    onClick={() => remove(field.name)}
                  />
                </Space>
              ))}
              <Button
                type="dashed"
                onClick={() => add({ key: "", value: "" })}
                icon={<PlusOutlined />}
                block
              >
                Add variable
              </Button>
            </>
          )}
        </Form.List>
      </Form>
      <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>
        Saving replaces the entire set — variables removed here are deleted.
      </Typography.Paragraph>
    </Drawer>
  );
}
