// serviceHelpers — the systemd unit helpers the Server Status services card
// and the admin Dashboard's services rail share (GH #1249), so both pages name,
// filter and colour a unit the same way.
import { Tag } from "antd";

import { CheckCircleOutlined, CloseOutlined, PauseCircleOutlined, SyncOutlined } from "@icons";

import type { ServerCapabilities } from "../../../hooks/useServerCapabilities";
import type { ServiceDetail } from "../../../hooks/useServerStatus";

// visibleServicesFor hides optional engines the operator hasn't enabled as a
// feature — an inactive postgresql/docker with Start/Enable buttons is noise
// when the server doesn't use them. Gate on the capability, not just runtime
// state. The admin Dashboard's services rail lists the same units (GH #1249).
export function visibleServicesFor(
  services: ServiceDetail[],
  caps: Pick<ServerCapabilities, "postgres_enabled" | "docker_marketplace_enabled"> | undefined,
): ServiceDetail[] {
  return services.filter((s) => {
    const name = s.unit.replace(/\.service$/, "");
    if (name === "postgresql" && !caps?.postgres_enabled) return false;
    if (name === "docker" && !caps?.docker_marketplace_enabled) return false;
    return true;
  });
}

export function prettyName(unit: string): string {
  const base = unit.replace(/\.service$/, "");
  return base.replace(/^jabali-/, "");
}

// isRunConfigured reports whether a unit's systemd UnitFileState means the
// operator expects it to be running (mirrors the backend's unitShouldBeRunning).
// disabled / indirect / masked / "" are intentionally-idle / on-demand units.
export function isRunConfigured(state?: string): boolean {
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
export function statusTag(service: ServiceDetail) {
  if (service.active === "inactive" && !isRunConfigured(service.unit_file_state)) {
    return (
      <Tag color="default" icon={<PauseCircleOutlined />} variant="filled">
        idle
      </Tag>
    );
  }
  return statusIcon(service.active);
}

export function statusIcon(state: string) {
  switch (state) {
    case "active":
      return <Tag color="green" icon={<CheckCircleOutlined />} variant="filled">active</Tag>;
    case "failed":
      return <Tag color="red" icon={<CloseOutlined />} variant="filled">failed</Tag>;
    case "inactive":
      return <Tag color="red" icon={<CloseOutlined />} variant="filled">inactive</Tag>;
    case "activating":
      return <Tag color="orange" icon={<SyncOutlined spin />} variant="filled">activating</Tag>;
    case "deactivating":
      return <Tag color="orange" icon={<SyncOutlined spin />} variant="filled">deactivating</Tag>;
    default:
      return <Tag color="default">{state}</Tag>;
  }
}
