// ServicesSummaryCard — compact Service / Status / Action table for the
// Server Status top section. Action column is a row of icon-only
// Buttons each wrapped in a Tooltip explaining the verb. Self-destruct
// panel-critical units (jabali-panel, jabali-agent, mariadb, nginx,
// redis-server) hide Stop+Disable —
// those requests are 403'd at the API anyway. Destructive verbs
// (stop/disable/restart) show a confirm Modal first.
import { useState } from "react";
import {
  Card,
  Modal,
  Table,
  Tag,
  message,
} from "antd";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import {
  CheckCircleOutlined,
  CloseOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  PoweroffOutlined,
  ReloadOutlined,
  SettingOutlined,
  SyncOutlined,
} from "@icons";
import { RowActions } from "../../../components/RowActions";

import { apiClient } from "../../../apiClient";
import { useServerCapabilities } from "../../../hooks/useServerCapabilities";
import type { ServiceDetail } from "../../../hooks/useServerStatus";

interface Props {
  services: ServiceDetail[];
}

const reloadCapable = new Set([
  "nginx.service",
  "pdns.service",
  "pdns-recursor.service",
]);

// Panel-critical units: stopping/disabling any of these from the UI would make
// the panel inaccessible (GH #746). Kept in sync with panelSelfDestructUnits in
// panel-api/internal/api/admin_services.go. nginx is the reverse proxy (stop ->
// 502); redis-server is a hard jabali-panel dependency (stop -> the panel dies).
const selfDestructUnits = new Set([
  "jabali-panel.service",
  "jabali-agent.service",
  "mariadb.service",
  "nginx.service",
  "redis-server.service",
]);

type Action = "restart" | "reload" | "start" | "stop" | "enable" | "disable";

const destructiveActions = new Set<Action>(["stop", "disable", "restart"]);

export function ServicesSummaryCard({ services }: Props) {
  const qc = useQueryClient();
  const { data: caps } = useServerCapabilities();
  // Hide optional engines the operator hasn't enabled as a feature — an
  // inactive postgresql/docker with Start/Enable buttons is noise when the
  // server doesn't use them. Gate on the capability, not just runtime state.
  const visibleServices = services.filter((s) => {
    const name = s.unit.replace(/\.service$/, "");
    if (name === "postgresql" && !caps?.postgres_enabled) return false;
    if (name === "docker" && !caps?.docker_marketplace_enabled) return false;
    return true;
  });
  const [pending, setPending] = useState<{ unit: string; action: Action } | null>(null);

  const ctl = useMutation({
    mutationFn: async ({ unit, action }: { unit: string; action: Action }) => {
      const name = unit.replace(/\.service$/, "");
      await apiClient.post(`/admin/services/${encodeURIComponent(name)}/${action}`);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "server-status"] });
      message.success("Done");
    },
    onError: (e: unknown) => {
      message.error(e instanceof Error ? e.message : "Service action failed");
    },
  });

  const runAction = (unit: string, action: Action) => {
    if (destructiveActions.has(action)) {
      setPending({ unit, action });
      return;
    }
    ctl.mutate({ unit, action });
  };

  const confirmPending = () => {
    if (!pending) return;
    ctl.mutate(pending);
    setPending(null);
  };

  return (
    <>
      <Card title={<><SettingOutlined /> Services</>} size="small">
        <Table<ServiceDetail>
          rowKey="unit"
          size="small"
          dataSource={visibleServices}
          pagination={false}
          scroll={{ x: "max-content" }}
          showHeader={false}
          columns={[
            {
              title: "Service",
              dataIndex: "unit",
              render: (u: string) => prettyName(u),
            },
            {
              title: "Status",
              dataIndex: "active",
              width: 120,
              render: (_: string, r: ServiceDetail) => statusTag(r),
            },
            {
              title: "Actions",
              width: 200,
              align: "right" as const,
              render: (_: unknown, r: ServiceDetail) => (
                <ServiceActions service={r} onAction={runAction} />
              ),
            },
          ]}
        />
      </Card>
      <Modal
        open={!!pending}
        title={pending ? `${capitalize(pending.action)} ${prettyName(pending.unit)}?` : ""}
        okText={pending ? capitalize(pending.action) : "OK"}
        okButtonProps={{ danger: true }}
        onOk={confirmPending}
        onCancel={() => setPending(null)}
      >
        {pending?.action === "stop" && (
          <p>This will halt the unit. Dependent panel features may stop working until you start it again.</p>
        )}
        {pending?.action === "disable" && (
          <p>This will prevent the unit from starting at boot. Combine with Stop if you also want it down right now.</p>
        )}
        {pending?.action === "restart" && (
          <p>Restart causes a brief drop in service. Continue?</p>
        )}
      </Modal>
    </>
  );
}

interface ServiceActionsProps {
  service: ServiceDetail;
  onAction: (unit: string, action: Action) => void;
}

function ServiceActions({ service, onAction }: ServiceActionsProps) {
  const isDown = service.active === "inactive" || service.active === "failed";
  const isReload = reloadCapable.has(service.unit);
  const isSelfDestruct = selfDestructUnits.has(service.unit);
  const isEnabled = isRunConfigured(service.unit_file_state);

  return (
    <RowActions
      actions={[
        { key: "start", label: "Start", icon: <PlayCircleOutlined />, hidden: !isDown, onClick: () => onAction(service.unit, "start"), tooltip: "Start" },
        { key: "restart", label: "Restart", icon: <SyncOutlined />, hidden: isDown, onClick: () => onAction(service.unit, "restart"), tooltip: "Restart" },
        { key: "reload", label: "Reload", icon: <ReloadOutlined />, hidden: isDown || !isReload, onClick: () => onAction(service.unit, "reload"), tooltip: "Reload" },
        { key: "stop", label: "Stop", icon: <PauseCircleOutlined />, danger: true, hidden: isDown || isSelfDestruct, onClick: () => onAction(service.unit, "stop"), tooltip: "Stop" },
        { key: "enable", label: "Enable at boot", icon: <PoweroffOutlined />, hidden: isEnabled, onClick: () => onAction(service.unit, "enable"), tooltip: "Enable at boot" },
        { key: "disable", label: "Disable at boot", icon: <PoweroffOutlined />, danger: true, hidden: !isEnabled || isSelfDestruct, onClick: () => onAction(service.unit, "disable"), tooltip: "Disable at boot" },
      ]}
    />
  );
}

function prettyName(unit: string): string {
  const base = unit.replace(/\.service$/, "");
  return base.replace(/^jabali-/, "");
}

// isRunConfigured reports whether a unit's systemd UnitFileState means the
// operator expects it to be running (mirrors the backend's unitShouldBeRunning).
// disabled / indirect / masked / "" are intentionally-idle / on-demand units.
function isRunConfigured(state?: string): boolean {
  return (
    state === "enabled" ||
    state === "enabled-runtime" ||
    state === "static" ||
    state === "alias"
  );
}

// statusTag renders the status cell. An inactive unit that isn't run-configured
// (disabled / on-demand — e.g. jabali-webmail, which the reconciler starts only
// once a domain enables email) is idle BY DESIGN, so show it neutral rather than
// as a red failure that reads like something broke.
function statusTag(service: ServiceDetail) {
  if (service.active === "inactive" && !isRunConfigured(service.unit_file_state)) {
    return (
      <Tag color="default" icon={<PauseCircleOutlined />} bordered={false}>
        idle
      </Tag>
    );
  }
  return statusIcon(service.active);
}

function statusIcon(state: string) {
  switch (state) {
    case "active":
      return <Tag color="green" icon={<CheckCircleOutlined />} bordered={false}>active</Tag>;
    case "failed":
      return <Tag color="red" icon={<CloseOutlined />} bordered={false}>failed</Tag>;
    case "inactive":
      return <Tag color="red" icon={<CloseOutlined />} bordered={false}>inactive</Tag>;
    case "activating":
      return <Tag color="orange" icon={<SyncOutlined spin />} bordered={false}>activating</Tag>;
    case "deactivating":
      return <Tag color="orange" icon={<SyncOutlined spin />} bordered={false}>deactivating</Tag>;
    default:
      return <Tag color="default">{state}</Tag>;
  }
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
