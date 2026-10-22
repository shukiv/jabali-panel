// Python Application Manager (ADR-0131 / GH #203) — user-shell page to
// register and control native Python web apps. Hidden behind the
// python_apps_enabled server setting; the API 403s when off. Node.js apps
// (ADR-0175, node_apps_enabled) share the page: the create dialog offers the
// runtimes that are on.
import { useTranslation } from "react-i18next";
import {
  App,
  Button,
  Form,
  Input,
  Modal,
  Radio,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { ReloadOutlined, PauseCircleOutlined, FileTextOutlined, DeleteOutlined, SettingOutlined, FolderOpenOutlined } from "@icons";
import { RowActions } from "../../../components/RowActions";
import { PythonAppEnvDrawer } from "./PythonAppEnvDrawer";
import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { CatalogCard, CatalogGrid, CategoryFilter } from "../../../components/catalog";
import { useServerCapabilities } from "../../../hooks/useServerCapabilities";
import { appManagerNavLabel } from "../../../nav";
import type { AppRuntime, CreateAppForm, Framework, PythonApp } from "./usePythonApps";
import {
  NODE_APP_VERSION,
  appRuntimes,
  createAppBody,
  fetchPythonAppLogs,
  nodeEntrypointRe,
  npmScriptRe,
  useControlPythonApp,
  useCreatePythonApp,
  useDeletePythonApp,
  useFrameworks,
  usePythonApps,
  usePythonVersions,
  useSetPythonAppStatic,
  versionsForFramework,
} from "./usePythonApps";

type DomainRow = { id: string; name: string };

const STATUS_COLOR: Record<string, string> = {
  running: "green",
  pending: "default",
  building: "processing",
  stopped: "default",
  failed: "red",
};

const PYTHON_APP_TYPES = [
  { value: "wsgi", label: "WSGI (gunicorn)" },
  { value: "asgi", label: "ASGI (uvicorn)" },
];

const NODE_APP_TYPES = [
  { value: "node", label: "node <script>" },
  { value: "npm", label: "npm run <script>" },
];

export function PythonAppsPage() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const caps = useServerCapabilities();
  const pythonOn = !!caps.data?.python_apps_enabled;
  const nodeOn = !!caps.data?.node_apps_enabled;
  const runtimes = appRuntimes(pythonOn, nodeOn);
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
  const [catTags, setCatTags] = useState<string[]>([]);
  const allFrameworkTags = useMemo(() => {
    const s = new Set<string>();
    (frameworks.data ?? []).forEach((f) => (f.tags ?? []).forEach((t) => s.add(t)));
    return [...s].sort();
  }, [frameworks.data]);
  const visibleFrameworks = useMemo(
    () =>
      (frameworks.data ?? []).filter(
        (f) => catTags.length === 0 || (f.tags ?? []).some((t) => catTags.includes(t)),
      ),
    [frameworks.data, catTags],
  );
  const [createOpen, setCreateOpen] = useState(false);
  // When set, the create modal is a framework install: app_type + entrypoint
  // are derived from the catalog entry, so those fields are hidden (JAB-164).
  const [installFw, setInstallFw] = useState<Framework | null>(null);
  // The create dialog's runtime; the choice shows only when both are on.
  const [runtime, setRuntime] = useState<AppRuntime>("python");
  const [form] = Form.useForm<CreateAppForm>();
  const appType = Form.useWatch("app_type", form);
  // A framework install only offers the runtimes its pinned deps support.
  const createVersionOptions = useMemo(
    () => versionsForFramework(versionOptions, installFw?.python_min),
    [versionOptions, installFw],
  );
  const openCreate = (fw?: Framework) => {
    form.resetFields();
    // The framework catalog is Python's.
    setRuntime(fw || pythonOn ? "python" : "node");
    setInstallFw(fw ?? null);
    setCreateOpen(true);
  };
  // Keep the app type one the runtime has. The dialog's form outlives it, so
  // this runs on open and on a runtime switch rather than via initialValues.
  useEffect(() => {
    if (!createOpen) return;
    const current = form.getFieldValue("app_type");
    const fits =
      runtime === "node"
        ? current === "node" || current === "npm"
        : current === "wsgi" || current === "asgi";
    if (!fits) {
      form.setFields([
        { name: "app_type", value: runtime === "node" ? "node" : "wsgi" },
        { name: "entrypoint", value: undefined, errors: [] },
      ]);
    }
  }, [createOpen, runtime, form]);
  // When the create dialog opens, default the Python version to the newest
  // interpreter actually installed on this host.
  useEffect(() => {
    if (!createOpen || runtime !== "python") return;
    const host = pyVersions.data?.default;
    const def =
      host && createVersionOptions.includes(host)
        ? host
        : createVersionOptions[createVersionOptions.length - 1];
    if (def && !form.getFieldValue("python_version")) {
      form.setFieldValue("python_version", def);
    }
  }, [createOpen, runtime, pyVersions.data, createVersionOptions, form]);
  const [logsApp, setLogsApp] = useState<PythonApp | null>(null);
  const [logsText, setLogsText] = useState("");
  const [envApp, setEnvApp] = useState<PythonApp | null>(null);
  // GH #878: static + media asset split editor (Passenger public/ equivalent).
  const [staticApp, setStaticApp] = useState<PythonApp | null>(null);
  const [staticForm] = Form.useForm<{
    static_url: string;
    static_root: string;
    media_url: string;
    media_root: string;
  }>();
  const setStatic = useSetPythonAppStatic();
  const openStatic = (app: PythonApp) => {
    staticForm.setFieldsValue({
      static_url: app.static_url ?? "",
      static_root: app.static_root ?? "",
      media_url: app.media_url ?? "",
      media_root: app.media_root ?? "",
    });
    setStaticApp(app);
  };
  const submitStatic = async () => {
    if (!staticApp) return;
    const v = await staticForm.validateFields();
    try {
      await setStatic.mutateAsync({
        id: staticApp.id,
        static_url: v.static_url?.trim() ?? "",
        static_root: v.static_root?.trim() ?? "",
        media_url: v.media_url?.trim() ?? "",
        media_root: v.media_root?.trim() ?? "",
      });
      message.success("Static & media mapping saved");
      setStaticApp(null);
    } catch (e) {
      message.error(e instanceof Error ? e.message : "Save failed");
    }
  };

  const submit = async () => {
    const values = await form.validateFields();
    try {
      // Framework install: send the slug; the server derives app_type +
      // entrypoint and scaffolds the starter.
      await create.mutateAsync(createAppBody(runtime, values, installFw?.slug));
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
          {t(appManagerNavLabel(pythonOn, nodeOn))}
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
        <Table.Column<PythonApp> title={t("pythonappspage.name")} dataIndex="name" />
        <Table.Column<PythonApp>
          title={t("pythonappspage.domain")}
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
          title={t("pythonappspage.runtime")}
          render={(_, r) => (
            <span>
              {r.runtime === "node" ? "Node.js" : "Python"} {r.python_version}{" "}
              <Tag>{r.app_type.toUpperCase()}</Tag>
            </span>
          )}
        />
        <Table.Column<PythonApp>
          title={t("pythonappspage.status")}
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
                { key: "static", label: "Static & media", icon: <FolderOpenOutlined />, onClick: () => openStatic(r) },
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
          // The framework catalog has Python apps only.
          ...(pythonOn ? [{
            key: "catalog",
            label: "Catalog",
            children: (
              <>
                <CategoryFilter
                  tags={allFrameworkTags}
                  selected={catTags}
                  onChange={setCatTags}
                />
                <CatalogGrid>
                  {visibleFrameworks.map((fw) => (
                    <CatalogCard
                      key={fw.slug}
                      name={fw.name}
                      iconNode={
                        <div
                          style={{
                            width: 44,
                            height: 44,
                            borderRadius: 10,
                            background: "#1f1f1f",
                            display: "flex",
                            alignItems: "center",
                            justifyContent: "center",
                            flexShrink: 0,
                            padding: 8,
                          }}
                        >
                          {fw.icon ? (
                            <img
                              src={fw.icon}
                              alt=""
                              style={{ maxWidth: "100%", maxHeight: "100%" }}
                            />
                          ) : null}
                        </div>
                      }
                      meta={<Tag color="blue">{fw.app_type.toUpperCase()}</Tag>}
                      description={fw.description}
                      onInstall={() => openCreate(fw)}
                    />
                  ))}
                  {frameworks.data && frameworks.data.length === 0 ? (
                    <Typography.Text type="secondary">
                      No frameworks available.
                    </Typography.Text>
                  ) : null}
                </CatalogGrid>
              </>
            ),
          }] : []),
        ]}
      />

      <Modal
        title={
          installFw
            ? `Install ${installFw.name}`
            : runtime === "node"
              ? "Create Node.js app"
              : "Create Python app"
        }
        open={createOpen}
        onCancel={() => setCreateOpen(false)}
        onOk={() => void submit()}
        okText={t("pythonappspage.create")}
        confirmLoading={create.isPending}
      >
        <Form form={form} layout="vertical" initialValues={{ app_type: "wsgi", base_uri: "/" }}>
          {runtimes.length > 1 && !installFw && (
            <Form.Item label="Runtime">
              <Radio.Group
                optionType="button"
                buttonStyle="solid"
                value={runtime}
                onChange={(e) => setRuntime(e.target.value as AppRuntime)}
                options={[
                  { value: "python", label: "Python" },
                  { value: "node", label: "Node.js" },
                ]}
              />
            </Form.Item>
          )}
          <Form.Item name="name" label={t("pythonappspage.name")} rules={[{ required: true }]}>
            <Input placeholder={t("pythonappspage.my_api")} />
          </Form.Item>
          <Form.Item name="domain_id" label={t("pythonappspage.domain")} rules={[{ required: true }]}>
            <Select
              loading={domains.isLoading}
              options={(domains.data ?? []).map((d) => ({ value: d.id, label: d.name }))}
              placeholder={t("pythonappspage.select_a_domain")}
            />
          </Form.Item>
          <Form.Item name="base_uri" label={t("pythonappspage.mount_path")} tooltip="'/' for the whole domain, or '/app' for a sub-path">
            <Input placeholder="/" />
          </Form.Item>
          <Space style={{ width: "100%" }} size="middle">
            {runtime === "node" ? (
              <Form.Item label="Node.js" tooltip="Node.js apps run on the server's Node.js 22">
                <Select
                  style={{ width: 120 }}
                  value={NODE_APP_VERSION}
                  disabled
                  options={[{ value: NODE_APP_VERSION, label: NODE_APP_VERSION }]}
                />
              </Form.Item>
            ) : (
              <Form.Item name="python_version" label={t("pythonappspage.python")} rules={[{ required: true }]}>
                <Select
                  style={{ width: 120 }}
                  loading={pyVersions.isLoading}
                  options={createVersionOptions.map((v) => ({ value: v, label: v }))}
                  notFoundContent={
                    installFw?.python_min
                      ? `${installFw.name} needs Python ${installFw.python_min} or newer, which is not installed on this server`
                      : "No Python runtime installed on this server"
                  }
                />
              </Form.Item>
            )}
            {!installFw && (
              <Form.Item name="app_type" label={t("pythonappspage.type")} rules={[{ required: true }]}>
                <Select
                  style={{ width: runtime === "node" ? 180 : 160 }}
                  options={runtime === "node" ? NODE_APP_TYPES : PYTHON_APP_TYPES}
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
          ) : runtime === "node" && appType === "npm" ? (
            <Form.Item
              key="npm-script"
              name="entrypoint"
              label="npm script"
              tooltip="A script in package.json, run as npm run <script>. Empty runs start."
              rules={[{ pattern: npmScriptRe, message: "Expected a package.json script name, like start" }]}
            >
              <Input placeholder="start" />
            </Form.Item>
          ) : runtime === "node" ? (
            <Form.Item
              key="node-entrypoint"
              name="entrypoint"
              label={t("pythonappspage.entrypoint")}
              tooltip="A .js, .mjs or .cjs file under the app directory, e.g. server.js or dist/main.js"
              rules={[
                { required: true },
                { pattern: nodeEntrypointRe, message: "Expected a .js, .mjs or .cjs file under the app directory" },
              ]}
            >
              <Input placeholder="server.js" />
            </Form.Item>
          ) : (
            <Form.Item
              key="python-entrypoint"
              name="entrypoint"
              label={t("pythonappspage.entrypoint")}
              tooltip="module:callable, e.g. myapp.wsgi:application or main:app"
              rules={[{ required: true }, { pattern: /^[A-Za-z0-9_.]+:[A-Za-z0-9_]+$/, message: "Expected module:callable" }]}
            >
              <Input placeholder="main:app" />
            </Form.Item>
          )}
          <Form.Item name="app_root" label={t("pythonappspage.app_directory")} tooltip={t("pythonappspage.path_under_your_home_e_g_domains_example_com")} rules={[{ required: true }]}>
            <Input placeholder="domains/example.com/app" />
          </Form.Item>
          {runtime === "node" && (
            <Typography.Paragraph type="secondary">
              The app must listen on <Typography.Text code>process.env.PORT</Typography.Text>; the
              panel sets PORT and HOST. With a package.json, the panel installs the dependencies
              (npm ci when there is a lockfile) and runs npm run build when there is a build script.
            </Typography.Paragraph>
          )}
          {!installFw && (
            <>
              <Space style={{ width: "100%" }} size="middle">
                <Form.Item
                  name="static_url"
                  label="Static URL path"
                  tooltip="Optional. Requests under this path are served by nginx directly instead of your app — the Passenger public/ equivalent. Set both fields or neither."
                >
                  <Input placeholder="/static" />
                </Form.Item>
                <Form.Item
                  name="static_root"
                  label="Static directory"
                  tooltip="Directory relative to the app directory that holds the static files, e.g. public or staticfiles."
                >
                  <Input placeholder="public" />
                </Form.Item>
              </Space>
              <Space style={{ width: "100%" }} size="middle">
                <Form.Item
                  name="media_url"
                  label="Media URL path"
                  tooltip="Optional. User-uploaded media (Django MEDIA_URL). Served by nginx directly from the media directory, with a short cache since uploads can change. Set both fields or neither."
                >
                  <Input placeholder="/media" />
                </Form.Item>
                <Form.Item
                  name="media_root"
                  label="Media directory"
                  tooltip="Directory relative to the app directory that holds uploaded media (Django MEDIA_ROOT), e.g. media."
                >
                  <Input placeholder="media" />
                </Form.Item>
              </Space>
            </>
          )}
        </Form>
      </Modal>

      <Modal
        title={staticApp ? `Static & media files — ${staticApp.name}` : "Static & media files"}
        open={staticApp !== null}
        onCancel={() => setStaticApp(null)}
        onOk={() => void submitStatic()}
        okText="Save"
        confirmLoading={setStatic.isPending}
      >
        <Typography.Paragraph type="secondary">
          nginx serves the URL paths below directly from directories inside your
          app — CSS/JS/images stop going through the Python process (what
          Passenger did with public/). Media is user-uploaded content (Django
          MEDIA_ROOT), cached briefly since uploads can change at the same URL.
          Clear both fields of a pair to proxy those requests to the app again.
        </Typography.Paragraph>
        <Form form={staticForm} layout="vertical">
          <Form.Item
            name="static_url"
            label="Static URL path"
            rules={[{ pattern: /^$|^\/[A-Za-z0-9._/-]+$/, message: "URL path like /static" }]}
          >
            <Input placeholder="/static" />
          </Form.Item>
          <Form.Item
            name="static_root"
            label="Static directory (relative to app directory)"
            rules={[{ pattern: /^$|^[A-Za-z0-9._-]+(?:\/[A-Za-z0-9._-]+)*$/, message: "Relative directory like public" }]}
          >
            <Input placeholder="public" />
          </Form.Item>
          <Form.Item
            name="media_url"
            label="Media URL path"
            rules={[{ pattern: /^$|^\/[A-Za-z0-9._/-]+$/, message: "URL path like /media" }]}
          >
            <Input placeholder="/media" />
          </Form.Item>
          <Form.Item
            name="media_root"
            label="Media directory (relative to app directory)"
            rules={[{ pattern: /^$|^[A-Za-z0-9._-]+(?:\/[A-Za-z0-9._-]+)*$/, message: "Relative directory like media" }]}
          >
            <Input placeholder="media" />
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
