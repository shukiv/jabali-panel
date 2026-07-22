// M46 — Database server admin ops UI (Server Settings ▸ Databases tab).
// Rendered as a sibling of DatabasesCard so the opt-in PostgreSQL
// lifecycle card stays untouched. Each M46 step adds a <Card> section
// here. Step 1: root / superuser password (ADR-0097).
//
// Icons go through the @icons shim (CONVENTIONS) — never
// @ant-design/icons.
import { useTranslation } from "react-i18next";
import {
  DatabaseOutlined,
  KeyOutlined,
  SettingOutlined,
  ToolOutlined,
} from "@icons";
import {
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Segmented,
  Select,
  Skeleton,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";

type Engine = "mariadb" | "postgres";

interface RootPasswordResponse {
  password: string;
}

const ENGINE_LABEL: Record<Engine, string> = {
  mariadb: "MariaDB (root)",
  postgres: "PostgreSQL (postgres)",
};

function RootPasswordSection() {
  const { t } = useTranslation();
  const [busy, setBusy] = useState<Engine | null>(null);
  const [revealed, setRevealed] = useState<{ engine: Engine; password: string } | null>(
    null,
  );

  const rotate = async (engine: Engine) => {
    setBusy(engine);
    try {
      const res = await apiClient.post<RootPasswordResponse>(
        "/admin/databases/root-password",
        { engine },
      );
      setRevealed({ engine, password: res.data.password });
    } catch (err) {
      message.error(
        `Could not rotate ${ENGINE_LABEL[engine]} password: ${
          err instanceof Error ? err.message : String(err)
        }`,
      );
    } finally {
      setBusy(null);
    }
  };

  return (
    <Card
      title={
        <Space>
          <KeyOutlined />
          Root / superuser password
        </Space>
      }
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Sets a break-glass password <strong>alongside</strong> the existing
        socket / peer authentication — the panel keeps connecting over the
        local socket either way, so this never locks the panel out. The
        password is shown once; store it now.
      </Typography.Paragraph>
      <Space wrap>
        {(Object.keys(ENGINE_LABEL) as Engine[]).map((engine) => (
          <Popconfirm
            key={engine}
            title={`Rotate the ${ENGINE_LABEL[engine]} password?`}
            description={t("databaseadminsections.the_previous_password_if_any_stops_working_i")}
            okText={t("databaseadminsections.rotate")}
            okButtonProps={{ danger: true }}
            onConfirm={() => rotate(engine)}
          >
            <Button danger loading={busy === engine}>
              Set / rotate {ENGINE_LABEL[engine]} password
            </Button>
          </Popconfirm>
        ))}
      </Space>

      <Typography.Paragraph
        type="secondary"
        style={{ marginTop: 12, marginBottom: 0, fontSize: 12 }}
      >
        Per-database <em>user</em> passwords (not the root/superuser) are
        rotated from the Databases page — each database user has a
        reveal-once “Password” action there.
      </Typography.Paragraph>

      <Modal
        open={revealed != null}
        title={
          revealed ? `New ${ENGINE_LABEL[revealed.engine]} password` : ""
        }
        onCancel={() => setRevealed(null)}
        onOk={() => setRevealed(null)}
        okText={t("databaseadminsections.i_saved_it")}
        cancelButtonProps={{ style: { display: "none" } }}
        maskClosable={false}
      >
        <Typography.Paragraph type="warning">
          This is shown <strong>once</strong>. It is not stored in the panel
          and cannot be retrieved later.
        </Typography.Paragraph>
        <Typography.Paragraph
          copyable={{ text: revealed?.password ?? "" }}
          code
          style={{ fontSize: 15, wordBreak: "break-all" }}
        >
          {revealed?.password}
        </Typography.Paragraph>
      </Modal>
    </Card>
  );
}

interface ConfigParam {
  name: string;
  kind: "int" | "bytes" | "bool" | "float";
  min: number;
  max: number;
  unit: string;
  restart_required: boolean;
  default: string;
  help: string;
  value: string;
}

function ConfigTunerSection() {
  const { t } = useTranslation();
  const [engine, setEngine] = useState<Engine>("mariadb");
  const [params, setParams] = useState<ConfigParam[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();

  const load = async (e: Engine) => {
    setLoading(true);
    setParams(null);
    try {
      const res = await apiClient.get<{ data: ConfigParam[] }>(
        `/admin/databases/config?engine=${e}`,
      );
      setParams(res.data.data);
      form.setFieldsValue(
        Object.fromEntries(res.data.data.map((p) => [p.name, p.value])),
      );
    } catch (err) {
      message.error(
        `Could not load ${e} config: ${
          err instanceof Error ? err.message : String(err)
        }`,
      );
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load(engine);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [engine]);

  const apply = async () => {
    const values = form.getFieldsValue();
    const settings: Record<string, string> = {};
    for (const p of params ?? []) {
      const v = values[p.name];
      if (v !== undefined && v !== null && String(v) !== "") {
        settings[p.name] = String(v);
      }
    }
    setSaving(true);
    try {
      await apiClient.put("/admin/databases/config", { engine, settings });
      message.success(`${engine} configuration applied.`);
      void load(engine);
    } catch (err) {
      message.error(
        `Apply failed: ${err instanceof Error ? err.message : String(err)}`,
      );
    } finally {
      setSaving(false);
    }
  };

  const anyRestart = (params ?? []).some((p) => p.restart_required);

  return (
    <Card
      title={
        <Space>
          <SettingOutlined />
          Database configuration
        </Space>
      }
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Curated, range-checked tuning only — no raw config editing. Changes
        are validated and rolled back automatically if the server fails to
        come back up. Keys marked <Tag color="orange">restart</Tag> bounce
        the service: every site’s DB connections drop for a few seconds.
      </Typography.Paragraph>

      <Segmented
        options={[
          { label: "MariaDB", value: "mariadb" },
          { label: "PostgreSQL", value: "postgres" },
        ]}
        value={engine}
        onChange={(v) => setEngine(v as Engine)}
        style={{ marginBottom: 16 }}
      />

      {loading || params == null ? (
        <Skeleton active paragraph={{ rows: 6 }} />
      ) : (
        <Form form={form} layout="vertical">
          {params.map((p) => (
            <Form.Item
              key={p.name}
              name={p.name}
              label={
                <Space size={4}>
                  <code>{p.name}</code>
                  {p.unit && (
                    <Typography.Text type="secondary">
                      ({p.unit})
                    </Typography.Text>
                  )}
                  {p.restart_required && <Tag color="orange">restart</Tag>}
                </Space>
              }
              help={p.help}
            >
              {p.kind === "bool" ? (
                <Select
                  options={
                    engine === "mariadb"
                      ? [
                          { value: "0", label: "Off (0)" },
                          { value: "1", label: "On (1)" },
                        ]
                      : [
                          { value: "off", label: "off" },
                          { value: "on", label: "on" },
                        ]
                  }
                  style={{ maxWidth: 220 }}
                />
              ) : p.kind === "int" || p.kind === "float" ? (
                <InputNumber
                  min={p.min}
                  max={p.max}
                  step={p.kind === "float" ? 0.1 : 1}
                  style={{ maxWidth: 260 }}
                />
              ) : (
                <Input
                  style={{ maxWidth: 260 }}
                  placeholder={`${p.default} (bytes; K/M/G suffix ok)`}
                />
              )}
            </Form.Item>
          ))}

          <Popconfirm
            title={`Apply ${engine} configuration?`}
            description={
              anyRestart
                ? "Some changed keys require a service restart — DB connections will drop briefly."
                : "Configuration will be reloaded."
            }
            okText={t("databaseadminsections.apply")}
            onConfirm={apply}
          >
            <Button type="primary" loading={saving}>
              Apply {engine} configuration
            </Button>
          </Popconfirm>
        </Form>
      )}
    </Card>
  );
}

function AdminDbConsoleSection() {
  const [busy, setBusy] = useState<string | null>(null);

  const open = async (path: string, label: string) => {
    setBusy(label);
    try {
      const res = await apiClient.post<{ redirect_url: string }>(path, {});
      window.open(res.data.redirect_url, "_blank", "noopener,noreferrer");
    } catch (err) {
      message.error(
        `Could not open ${label}: ${
          err instanceof Error ? err.message : String(err)
        }`,
      );
    } finally {
      setBusy(null);
    }
  };

  return (
    <Card
      title={
        <Space>
          <DatabaseOutlined />
          Database console (all databases)
        </Space>
      }
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Opens a privileged session that can see and edit{" "}
        <strong>every database on this server</strong>. This is a
        root-equivalent web shell — single-use, short-lived, admin-only,
        and audited. Treat it accordingly.
      </Typography.Paragraph>
      <Space wrap>
        <Button
          loading={busy === "phpMyAdmin"}
          onClick={() => open("/admin/databases/sso/phpmyadmin", "phpMyAdmin")}
        >
          Open phpMyAdmin (all MariaDB)
        </Button>
        <Button
          loading={busy === "Adminer"}
          onClick={() => open("/admin/databases/sso/adminer", "Adminer")}
        >
          Open Adminer (all PostgreSQL)
        </Button>
      </Space>
    </Card>
  );
}

interface MaintenanceJob {
  id: string;
  status: "running" | "ok" | "error";
  summary?: string;
  engine: string;
  scope: string;
}

function MaintenanceSection() {
  const [engine, setEngine] = useState<Engine>("mariadb");
  const [running, setRunning] = useState(false);
  const [job, setJob] = useState<MaintenanceJob | null>(null);

  const poll = async (id: string) => {
    for (let i = 0; i < 120; i++) {
      await new Promise((r) => setTimeout(r, 3000));
      try {
        const res = await apiClient.get<MaintenanceJob>(
          `/admin/databases/maintenance/${id}`,
        );
        setJob(res.data);
        if (res.data.status !== "running") return;
      } catch {
        /* keep polling */
      }
    }
  };

  const run = async () => {
    setRunning(true);
    setJob(null);
    try {
      const res = await apiClient.post<{ job_id: string }>(
        "/admin/databases/maintenance",
        { engine, scope: "all" },
      );
      void poll(res.data.job_id);
      message.success("Maintenance started.");
    } catch (err) {
      const status = (err as { response?: { status?: number } })?.response
        ?.status;
      message.error(
        status === 409
          ? "A maintenance job is already running for this engine."
          : `Could not start maintenance: ${
              err instanceof Error ? err.message : String(err)
            }`,
      );
    } finally {
      setRunning(false);
    }
  };

  return (
    <Card
      title={
        <Space>
          <ToolOutlined />
          Maintenance (optimize &amp; analyze)
        </Space>
      }
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Runs <code>OPTIMIZE</code> + <code>ANALYZE</code> (MariaDB) or{" "}
        <code>VACUUM (ANALYZE)</code> + <code>REINDEX</code> (PostgreSQL)
        across all databases. Note: classic “repair” is a no-op on InnoDB
        (almost every database here) — this reclaims space and refreshes
        planner statistics, it does not “repair” InnoDB tables.
      </Typography.Paragraph>
      <Space wrap style={{ marginBottom: 12 }}>
        <Segmented
          options={[
            { label: "MariaDB", value: "mariadb" },
            { label: "PostgreSQL", value: "postgres" },
          ]}
          value={engine}
          onChange={(v) => setEngine(v as Engine)}
        />
        <Button
          type="primary"
          loading={running || job?.status === "running"}
          onClick={run}
        >
          Run maintenance (all {engine} databases)
        </Button>
        {job && (
          <Tag
            color={
              job.status === "ok"
                ? "green"
                : job.status === "error"
                  ? "red"
                  : "blue"
            }
          >
            {job.status}
          </Tag>
        )}
      </Space>
      {job?.summary && (
        <Typography.Paragraph
          code
          style={{
            whiteSpace: "pre-wrap",
            maxHeight: 220,
            overflow: "auto",
            fontSize: 12,
          }}
        >
          {job.summary}
        </Typography.Paragraph>
      )}
    </Card>
  );
}

interface DbProc {
  id: string;
  user: string;
  host: string;
  db: string;
  command: string;
  time: string;
  state: string;
  info: string;
}

function ProcessesSection() {
  const { t } = useTranslation();
  const [engine, setEngine] = useState<Engine>("mariadb");
  const [rows, setRows] = useState<DbProc[]>([]);
  const [loading, setLoading] = useState(false);

  const refresh = async (e: Engine) => {
    setLoading(true);
    try {
      const res = await apiClient.get<{ data: DbProc[] }>(
        `/admin/databases/processes?engine=${e}`,
      );
      setRows(res.data.data ?? []);
    } catch {
      /* transient; keep last view */
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void refresh(engine);
    const t = setInterval(() => void refresh(engine), 3000);
    return () => clearInterval(t);

  }, [engine]);

  const kill = async (id: string) => {
    try {
      await apiClient.post("/admin/databases/processes/kill", { engine, id });
      message.success(`Signalled ${id}.`);
      void refresh(engine);
    } catch (err) {
      message.error(
        `Kill failed: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
  };

  return (
    <Card
      title={
        <Space>
          <DatabaseOutlined />
          Database processes
        </Space>
      }
      extra={
        <Segmented
          options={[
            { label: "MariaDB", value: "mariadb" },
            { label: "PostgreSQL", value: "postgres" },
          ]}
          value={engine}
          onChange={(v) => setEngine(v as Engine)}
        />
      }
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Live server processes (auto-refreshes every 3s).{" "}
        {engine === "mariadb" ? "KILL" : "pg_terminate_backend"} ends the
        connection — every kill is audited.
      </Typography.Paragraph>
      <Table<DbProc>
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={rows}
        pagination={false}
        scroll={{ x: "max-content", y: 320 }}
      >
        <Table.Column dataIndex="id" title="ID" />
        <Table.Column dataIndex="user" title={t("databaseadminsections.user")} />
        <Table.Column dataIndex="host" title={t("databaseadminsections.host")} />
        <Table.Column dataIndex="db" title="DB" />
        {engine === "mariadb" && (
          <Table.Column dataIndex="command" title={t("databaseadminsections.command")} />
        )}
        {engine === "mariadb" && (
          <Table.Column dataIndex="time" title={t("databaseadminsections.time")} />
        )}
        <Table.Column dataIndex="state" title={t("databaseadminsections.state")} />
        <Table.Column
          dataIndex="info"
          title={t("databaseadminsections.query")}
          width={380}
          render={(v: string) => (
            // Bounded + ellipsis so a long query can't blow out the
            // card width (the Query column was clipping at the edge);
            // full text on hover via the native title tooltip.
            <span
              title={v}
              style={{
                fontFamily: "monospace",
                fontSize: 12,
                display: "block",
                maxWidth: 360,
                overflow: "hidden",
                textOverflow: "ellipsis",
                whiteSpace: "nowrap",
              }}
            >
              {v}
            </span>
          )}
        />
        <Table.Column
          title={t("databaseadminsections.actions")}
          render={(_, row: DbProc) => (
            <Popconfirm
              title={`Kill ${row.id}?`}
              okText={t("databaseadminsections.kill")}
              okButtonProps={{ danger: true }}
              onConfirm={() => kill(row.id)}
            >
              <Button danger size="small">
                Kill
              </Button>
            </Popconfirm>
          )}
        />
      </Table>
    </Card>
  );
}

export function DatabaseAdminSections() {
  return (
    <>
      <RootPasswordSection />
      <ConfigTunerSection />
      <AdminDbConsoleSection />
      <MaintenanceSection />
      <ProcessesSection />
    </>
  );
}
