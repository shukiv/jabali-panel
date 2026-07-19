// Python Application Manager (ADR-0131 / GH #203) — user-shell page to
// register and control native Python web apps. Hidden behind the
// python_apps_enabled server setting; the API 403s when off.
import {
  App,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { ReloadOutlined, PauseCircleOutlined, FileTextOutlined, DeleteOutlined, SettingOutlined } from "@icons";
import { RowActions } from "../../../components/RowActions";
import { PythonAppEnvDrawer } from "./PythonAppEnvDrawer";
import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import type { CreatePythonAppInput, Framework, PythonApp } from "./usePythonApps";
import {
  fetchPythonAppLogs,
  useControlPythonApp,
  useCreatePythonApp,
  useDeletePythonApp,
  useFrameworks,
  usePythonApps,
  usePythonVersions,
} from "./usePythonApps";

type DomainRow = { id: string; name: string };

const STATUS_COLOR: Record<string, string> = {
  running: "green",
  pending: "default",
  building: "processing",
  stopped: "default",
  failed: "red",
};

export function PythonAppsPage() {
  const { message } = App.useApp();
  const apps = usePythonApps();
  const create = useCreatePythonApp();
  const pyVersions = usePythonVersions();
  // Only offer installed interpreters; fall back to the common trio only if
  // the probe is unavailable (the API still gates the final choice, GH #357).
  const versionOptions = useMemo(
    () =>
      pyVersions.data?.versions && pyVersions.data.versions.length > 0
        ? pyVersions.data.versions
        : ["3.11", "3.12", "3.13"],
    [pyVersions.data],
  );
  const del = useDeletePythonApp();
  const control = useControlPythonApp();

  const domains = useQuery({
    queryKey: ["domains", "for-pyapps"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: DomainRow[] }>("/domains");
      return data.data ?? [];
    },
  });
  const domainName = useMemo(() => {
    const m = new Map<string, string>();
    (domains.data ?? []).forEach((d) => m.set(d.id, d.name));
    return m;
  }, [domains.data]);

  const frameworks = useFrameworks();
  const [createOpen, setCreateOpen] = useState(false);
  // When set, the create modal is a framework install: app_type + entrypoint
  // are derived from the catalog entry, so those fields are hidden (JAB-164).
  const [installFw, setInstallFw] = useState<Framework | null>(null);
  const [form] = Form.useForm<CreatePythonAppInput>();
  const openCreate = (fw?: Framework) => {
    form.resetFields();
    setInstallFw(fw ?? null);
    setCreateOpen(true);
  };
  // When the create dialog opens, default the Python version to the newest
  // interpreter actually installed on this host.
  useEffect(() => {
    if (!createOpen) return;
    const def =
      pyVersions.data?.default || versionOptions[versionOptions.length - 1];
    if (def && !form.getFieldValue("python_version")) {
      form.setFieldValue("python_version", def);
    }
  }, [createOpen, pyVersions.data, versionOptions, form]);
  const [logsApp, setLogsApp] = useState<PythonApp | null>(null);
  const [logsText, setLogsText] = useState("");
  const [envApp, setEnvApp] = useState<PythonApp | null>(null);

  const submit = async () => {
    const values = await form.validateFields();
    try {
      await create.mutateAsync({
        ...values,
        base_uri: values.base_uri || "/",
        // Framework install: send the slug; the server derives app_type +
        // entrypoint and scaffolds the starter.
        ...(installFw ? { framework: installFw.slug } : {}),
      });
      message.success(
        installFw ? `Installing ${installFw.name}…` : "App created — building…",
      );
      setCreateOpen(false);
      form.resetFields();
    } catch (e) {
      message.error(e instanceof Error ? e.message : "Create failed");
    }
  };

  const doControl = async (id: string, action: string) => {
    try {
      await control.mutateAsync({ id, action });
      message.success(`${action} requested`);
    } catch (e) {
      message.error(e instanceof Error ? e.message : `${action} failed`);
    }
  };

  const openLogs = async (app: PythonApp) => {
    setLogsApp(app);
    setLogsText("Loading…");
    try {
      setLogsText((await fetchPythonAppLogs(app.id)) || "(no output)");
    } catch (e) {
      setLogsText(e instanceof Error ? e.message : "Failed to load logs");
    }
  };

  return (
    <div>
      <Space
        style={{ width: "100%", justifyContent: "space-between", marginBottom: 16 }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          Python Apps
        </Typography.Title>
        <Button type="primary" onClick={() => openCreate()}>
          Create app
        </Button>
      </Space>

      <Tabs
        defaultActiveKey="installed"
        items={[
          {
            key: "installed",
            label: "Installed",
            children: (
              <Table<PythonApp>
        rowKey="id"
        dataSource={apps.data ?? []}
        loading={apps.isLoading}
        pagination={false}
      >
        <Table.Column<PythonApp> title="Name" dataIndex="name" />
        <Table.Column<PythonApp>
          title="Domain"
          render={(_, r) => (
            <Typography.Text>
              {domainName.get(r.domain_id) ?? "—"}
              {r.base_uri !== "/" ? (
                <Typography.Text type="secondary"> {r.base_uri}</Typography.Text>
              ) : null}
            </Typography.Text>
          )}
        />
        <Table.Column<PythonApp>
          title="Runtime"
          render={(_, r) => (
            <span>
              Python {r.python_version}{" "}
              <Tag>{r.app_type.toUpperCase()}</Tag>
            </span>
          )}
        />
        <Table.Column<PythonApp>
          title="Status"
          render={(_, r) => {
            const tag = <Tag color={STATUS_COLOR[r.status] ?? "default"}>{r.status}</Tag>;
            // GH #357: a failed app used to show just "failed" with no reason
            // ("nothing in logs") — surface last_error on hover, like Docker apps.
            return r.status === "failed" && r.last_error ? (
              <Tooltip title={r.last_error}>{tag}</Tooltip>
            ) : (
              tag
            );
          }}
        />
        <Table.Column<PythonApp>
          title=""
          width={280}
          render={(_, r) => (
            <RowActions
              actions={[
                { key: "restart", label: "Restart", icon: <ReloadOutlined />, onClick: () => void doControl(r.id, "restart") },
                { key: "stop", label: "Stop", icon: <PauseCircleOutlined />, onClick: () => void doControl(r.id, "stop") },
                { key: "logs", label: "Logs", icon: <FileTextOutlined />, onClick: () => void openLogs(r) },
                { key: "env", label: "Environment", icon: <SettingOutlined />, onClick: () => setEnvApp(r) },
                {
                  key: "delete",
                  label: "Delete",
                  icon: <DeleteOutlined />,
                  danger: true,
                  onClick: () => void del.mutateAsync(r.id),
                  confirm: { title: "Delete app?", description: "Stops the service and removes it. App files are kept.", okText: "Delete" },
                },
              ]}
            />
          )}
        />
              </Table>
            ),
          },
          {
            key: "catalog",
            label: "Catalog",
            children: (
              <div
                style={{
                  display: "grid",
                  gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))",
                  gap: 16,
                }}
              >
                {(frameworks.data ?? []).map((fw) => (
                  <Card
                    key={fw.slug}
                    size="small"
                    title={
                      <Space size={8}>
                        {fw.icon ? (
                          <span
                            style={{
                              display: "inline-flex",
                              alignItems: "center",
                              justifyContent: "center",
                              width: 28,
                              height: 28,
                              borderRadius: 6,
                              background: "#1f1f1f",
                              padding: 4,
                            }}
                          >
                            <img
                              src={fw.icon}
                              alt=""
                              style={{ maxWidth: "100%", maxHeight: "100%" }}
                            />
                          </span>
                        ) : null}
                        <span>{fw.name}</span>
                      </Space>
                    }
                    extra={<Tag color="blue">{fw.app_type.toUpperCase()}</Tag>}
                    actions={[
                      <Button
                        key="install"
                        type="link"
                        onClick={() => openCreate(fw)}
                      >
                        Install
                      </Button>,
                    ]}
                  >
                    <Typography.Paragraph
                      type="secondary"
                      style={{ minHeight: 44, marginBottom: 8 }}
                    >
                      {fw.description}
                    </Typography.Paragraph>
                    <Space size={4} wrap>
                      {(fw.tags ?? []).map((t) => (
                        <Tag key={t}>{t}</Tag>
                      ))}
                    </Space>
                  </Card>
                ))}
                {frameworks.data && frameworks.data.length === 0 ? (
                  <Typography.Text type="secondary">
                    No frameworks available.
                  </Typography.Text>
                ) : null}
              </div>
            ),
          },
        ]}
      />

      <Modal
        title={installFw ? `Install ${installFw.name}` : "Create Python app"}
        open={createOpen}
        onCancel={() => setCreateOpen(false)}
        onOk={() => void submit()}
        okText="Create"
        confirmLoading={create.isPending}
      >
        <Form form={form} layout="vertical" initialValues={{ app_type: "wsgi", base_uri: "/" }}>
          <Form.Item name="name" label="Name" rules={[{ required: true }]}>
            <Input placeholder="My API" />
          </Form.Item>
          <Form.Item name="domain_id" label="Domain" rules={[{ required: true }]}>
            <Select
              loading={domains.isLoading}
              options={(domains.data ?? []).map((d) => ({ value: d.id, label: d.name }))}
              placeholder="Select a domain"
            />
          </Form.Item>
          <Form.Item name="base_uri" label="Mount path" tooltip="'/' for the whole domain, or '/app' for a sub-path">
            <Input placeholder="/" />
          </Form.Item>
          <Space style={{ width: "100%" }} size="middle">
            <Form.Item name="python_version" label="Python" rules={[{ required: true }]}>
              <Select
                style={{ width: 120 }}
                loading={pyVersions.isLoading}
                options={versionOptions.map((v) => ({ value: v, label: v }))}
                notFoundContent="No Python runtime installed on this server"
              />
            </Form.Item>
            {!installFw && (
              <Form.Item name="app_type" label="Type" rules={[{ required: true }]}>
                <Select
                  style={{ width: 160 }}
                  options={[
                    { value: "wsgi", label: "WSGI (gunicorn)" },
                    { value: "asgi", label: "ASGI (uvicorn)" },
                  ]}
                />
              </Form.Item>
            )}
          </Space>
          {installFw ? (
            <Typography.Paragraph type="secondary">
              {installFw.name} ({installFw.app_type.toUpperCase()} via{" "}
              {installFw.server}) will be scaffolded into the app directory —
              starter project, pinned dependencies
              {installFw.needs_db && installFw.needs_db !== "none"
                ? `, and a ${installFw.needs_db} database`
                : ""}
              .
            </Typography.Paragraph>
          ) : (
            <Form.Item
              name="entrypoint"
              label="Entrypoint"
              tooltip="module:callable, e.g. myapp.wsgi:application or main:app"
              rules={[{ required: true }, { pattern: /^[A-Za-z0-9_.]+:[A-Za-z0-9_]+$/, message: "Expected module:callable" }]}
            >
              <Input placeholder="main:app" />
            </Form.Item>
          )}
          <Form.Item name="app_root" label="App directory" tooltip="Path under your home, e.g. domains/example.com/app" rules={[{ required: true }]}>
            <Input placeholder="domains/example.com/app" />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={logsApp ? `Logs — ${logsApp.name}` : "Logs"}
        open={logsApp !== null}
        onCancel={() => setLogsApp(null)}
        footer={null}
        width={800}
      >
        <pre style={{ maxHeight: 480, overflow: "auto", fontSize: 12, whiteSpace: "pre-wrap" }}>
          {logsText}
        </pre>
      </Modal>

      <PythonAppEnvDrawer app={envApp} onClose={() => setEnvApp(null)} />
    </div>
  );
}
