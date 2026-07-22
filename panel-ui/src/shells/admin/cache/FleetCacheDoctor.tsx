// FleetCacheDoctor — JAB-11 (fleet). Admin controls for the cache-doctor /
// repair / refresh sweep across all cache-enabled WordPress installs. The sweep
// is long (one agent call per install), so the POST returns a run id and this
// polls it — no synchronous fleet call that would 502. Shows live progress,
// per-install results, and recent run history.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import {
  App,
  Alert,
  Button,
  Card,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { ReloadOutlined, ThunderboltOutlined, ToolOutlined } from "@icons";

import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";
import { shortDateTime } from "../../../utils/datetime";

type Kind = "doctor" | "repair" | "refresh";

type DoctorItem = {
  install_id: string;
  path: string;
  host?: string;
  healthy: boolean;
  drifted: boolean;
  repaired?: boolean;
  refreshed?: boolean;
  error?: string;
};

type DoctorRun = {
  id: string;
  kind: Kind;
  state: "queued" | "running" | "done" | "failed";
  triggered_by: string;
  total: number;
  checked: number;
  drifted: number;
  repaired: number;
  refreshed: number;
  failed: number;
  first_error: string;
  results?: DoctorItem[];
  started_at?: string | null;
  finished_at?: string | null;
  created_at: string;
};

const KIND_LABEL: Record<Kind, string> = {
  doctor: "Doctor",
  repair: "Repair",
  refresh: "Refresh plugin",
};

function stateColor(state: DoctorRun["state"]): string {
  return state === "done"
    ? "green"
    : state === "failed"
      ? "red"
      : state === "running"
        ? "blue"
        : "default";
}

const itemColumns: ColumnsType<DoctorItem> = [
  {
    title: "Site",
    key: "site",
    render: (_, r) => (
      <Space direction="vertical" size={0}>
        <Typography.Text>{r.host || r.install_id}</Typography.Text>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {r.path}
        </Typography.Text>
      </Space>
    ),
  },
  {
    title: "Status",
    key: "status",
    render: (_, r) => {
      if (r.refreshed) return <Tag color="green">Refreshed</Tag>;
      if (r.repaired) return <Tag color="green">Repaired</Tag>;
      if (r.drifted) return <Tag color="red">Drifted</Tag>;
      if (r.healthy) return <Tag color="green">Healthy</Tag>;
      return <Tag color="orange">Unhealthy</Tag>;
    },
  },
  {
    title: "Detail / next action",
    dataIndex: "error",
    key: "error",
    render: (err: string | undefined, r) =>
      err ? (
        <Typography.Text type={r.repaired || r.refreshed ? "secondary" : "danger"} style={{ fontSize: 12 }}>
          {err}
          {r.drifted && !r.repaired ? " — run Repair, or toggle the site's cache off/on." : ""}
        </Typography.Text>
      ) : (
        <Typography.Text type="secondary">—</Typography.Text>
      ),
  },
];

export function FleetCacheDoctor() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();
  const [activeRunId, setActiveRunId] = useState<string | null>(null);

  const history = useQuery({
    queryKey: ["cache-doctor-runs"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ runs: DoctorRun[] }>(`/admin/cache/doctor-runs`);
      return data.runs ?? [];
    },
    refetchOnWindowFocus: false,
  });

  const activeRun = useQuery({
    queryKey: ["cache-doctor-run", activeRunId],
    queryFn: async () => {
      const { data } = await apiClient.get<{ run: DoctorRun }>(
        `/admin/cache/doctor-runs/${activeRunId}`,
      );
      return data.run;
    },
    enabled: !!activeRunId,
    // Poll while the run is in flight; stop once terminal.
    refetchInterval: (query) => {
      const st = query.state.data?.state;
      return st === "done" || st === "failed" ? false : 2500;
    },
  });

  // When the active run finishes, refresh the history table once.
  const runState = activeRun.data?.state;
  useEffect(() => {
    if (runState === "done" || runState === "failed") {
      void qc.invalidateQueries({ queryKey: ["cache-doctor-runs"] });
    }
  }, [runState, qc]);

  const startMutation = useMutation({
    mutationFn: async (kind: Kind) => {
      const { data } = await apiClient.post<{ id: string }>(`/admin/cache/doctor-runs`, { kind });
      return data.id;
    },
    onSuccess: (id, kind) => {
      setActiveRunId(id);
      message.success(`${KIND_LABEL[kind]} sweep started`);
    },
    onError: (e) => {
      const detail = extractApiError(e);
      message.error(
        detail?.includes("run_in_progress")
          ? "A sweep is already running — wait for it to finish."
          : (detail ?? "Failed to start sweep"),
      );
    },
  });

  const run = activeRun.data;
  const busy = startMutation.isPending || run?.state === "running" || run?.state === "queued";

  return (
    <Card
      title={
        <Space>
          <ThunderboltOutlined />
          <span>Fleet cache doctor</span>
        </Space>
      }
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary">
        Sweep every cache-enabled WordPress install. <b>Doctor</b> detects drift
        (plugin inactive, missing drop-in/constants, unreachable Redis) read-only.
        <b> Repair</b> re-provisions drifted installs. <b>Refresh plugin</b> updates
        the jabali-cache plugin fleet-wide. Sweeps run in the background — progress
        updates below.
      </Typography.Paragraph>

      <Space wrap style={{ marginBottom: 12 }}>
        <Button
          type="primary"
          icon={<ThunderboltOutlined />}
          loading={busy && run?.kind === "doctor"}
          disabled={busy}
          onClick={() => startMutation.mutate("doctor")}
        >
          Run doctor
        </Button>
        <Popconfirm
          title={t("fleetcachedoctor.repair_all_drifted_installs")}
          description={t("fleetcachedoctor.re_provisions_cache_constants_acl_drop_in_fo")}
          okText={t("fleetcachedoctor.repair")}
          okButtonProps={{ danger: true }}
          disabled={busy}
          onConfirm={() => startMutation.mutate("repair")}
        >
          <Button danger icon={<ToolOutlined />} disabled={busy}>
            Repair drift
          </Button>
        </Popconfirm>
        <Popconfirm
          title={t("fleetcachedoctor.refresh_the_jabali_cache_plugin_on_every_cac")}
          okText={t("fleetcachedoctor.refresh")}
          disabled={busy}
          onConfirm={() => startMutation.mutate("refresh")}
        >
          <Button icon={<ReloadOutlined />} disabled={busy}>
            Refresh plugin
          </Button>
        </Popconfirm>
      </Space>

      {run ? (
        <div style={{ marginBottom: 16 }}>
          <Space wrap>
            <Tag color={stateColor(run.state)}>{run.state.toUpperCase()}</Tag>
            <span>{KIND_LABEL[run.kind]}</span>
            <Typography.Text type="secondary">
              checked {run.checked}/{run.total}
              {run.drifted ? `, drifted ${run.drifted}` : ""}
              {run.repaired ? `, repaired ${run.repaired}` : ""}
              {run.refreshed ? `, refreshed ${run.refreshed}` : ""}
              {run.failed ? `, failed ${run.failed}` : ""}
            </Typography.Text>
          </Space>
          {run.first_error ? (
            <Alert
              type="error"
              showIcon
              style={{ marginTop: 8 }}
              message={run.first_error}
            />
          ) : null}
          {run.results && run.results.length > 0 ? (
            <Table<DoctorItem>
              rowKey="install_id"
              size="small"
              style={{ marginTop: 12 }}
              columns={itemColumns}
              dataSource={run.results}
              pagination={{ pageSize: 10, hideOnSinglePage: true }}
            />
          ) : null}
        </div>
      ) : null}

      <Typography.Title level={5}>Recent runs</Typography.Title>
      <Table<DoctorRun>
        rowKey="id"
        size="small"
        loading={history.isLoading}
        dataSource={history.data ?? []}
        pagination={{ pageSize: 10, hideOnSinglePage: true }}
        onRow={(r) => ({ onClick: () => setActiveRunId(r.id), style: { cursor: "pointer" } })}
        columns={[
          { title: "When", dataIndex: "created_at", render: (v: string) => shortDateTime(v) },
          { title: "Kind", dataIndex: "kind", render: (k: Kind) => KIND_LABEL[k] },
          {
            title: "State",
            dataIndex: "state",
            render: (s: DoctorRun["state"]) => <Tag color={stateColor(s)}>{s}</Tag>,
          },
          {
            title: "Result",
            key: "result",
            render: (_, r) => (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {r.checked}/{r.total} checked
                {r.drifted ? `, ${r.drifted} drifted` : ""}
                {r.repaired ? `, ${r.repaired} repaired` : ""}
                {r.refreshed ? `, ${r.refreshed} refreshed` : ""}
                {r.failed ? `, ${r.failed} failed` : ""}
              </Typography.Text>
            ),
          },
        ]}
      />
    </Card>
  );
}
