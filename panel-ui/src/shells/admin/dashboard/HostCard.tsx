// HostCard — the admin Dashboard's host banner (GH #1249, the Option 1 base
// of the community-mix layout): which box this is (hostname, health roll-up,
// public IPs, OS · panel build · uptime) and the three meters an operator
// checks first. Everything deeper stays on Server Status, one click away.
import { Button, Card, Col, Progress, Row, Space, Tag, Typography } from "antd";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";

import { ServerOutlined } from "@icons";

import type { ServerCapabilities } from "../../../hooks/useServerCapabilities";
import type { ServerStatusEnvelope } from "../../../hooks/useServerStatus";
import { diskColor, diskUsePct, usageColor } from "../server-status/usage";

interface Props {
  env: ServerStatusEnvelope | undefined;
  caps: ServerCapabilities | undefined;
  /** The running panel build's commit, from the Updates Center state. */
  currentSha?: string;
}

interface MeterProps {
  label: string;
  /** null while the slice is missing or still warming up. */
  pct: number | null;
  color: (pct: number) => string;
}

function Meter({ label, pct, color }: MeterProps) {
  return (
    <div>
      <div style={{ display: "flex", justifyContent: "space-between" }}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {label}
        </Typography.Text>
        <Typography.Text strong style={{ fontSize: 12 }}>
          {pct == null ? "—" : `${pct}%`}
        </Typography.Text>
      </div>
      <Progress
        percent={pct ?? 0}
        showInfo={false}
        size="small"
        strokeColor={pct == null ? undefined : color(pct)}
        aria-label={label}
      />
    </div>
  );
}

export function HostCard({ env, caps, currentSha }: Props) {
  const { t } = useTranslation();
  const host = env?.host ?? null;
  const alerts = env?.alerts ?? [];
  const critical = alerts.filter((a) => a.level === "critical").length;
  const warnings = alerts.filter((a) => a.level === "warning").length;

  let healthTag = <Tag color="green">{t("dashboard.health.healthy")}</Tag>;
  if (critical > 0)
    healthTag = <Tag color="red">{t("dashboard.health.critical", { count: critical })}</Tag>;
  else if (warnings > 0)
    healthTag = <Tag color="orange">{t("dashboard.health.warning", { count: warnings })}</Tag>;

  const cpu = env?.cpu;
  const cpuPct = cpu && !cpu.warming_up ? Math.round(cpu.usage_percent) : null;
  const memPct =
    host && host.mem_total_kb > 0 ? Math.round((host.mem_used_kb / host.mem_total_kb) * 100) : null;
  const root = host?.partitions?.find((p) => p.mount_point === "/") ?? host?.partitions?.[0];
  const diskPct = root ? diskUsePct(root) : null;

  const facts = [
    host?.os,
    currentSha ? t("dashboard.host.build", { sha: currentSha.slice(0, 7) }) : null,
    host ? uptimeText(host.uptime_seconds, t) : null,
  ].filter(Boolean);

  return (
    <Card style={{ marginBottom: 16 }}>
      <Row gutter={[24, 16]} align="middle">
        <Col xs={24} lg={9}>
          <Space orientation="vertical" size={6} style={{ width: "100%" }}>
            <Space size={8} wrap>
              <Typography.Title level={4} style={{ margin: 0, wordBreak: "break-all" }}>
                {host?.hostname ?? "—"}
              </Typography.Title>
              {/* The health tag opens the Alerts tab, which lists what it counts. */}
              <Link to="?tab=alerts">{healthTag}</Link>
            </Space>
            {caps?.public_ipv4 || caps?.public_ipv6 ? (
              <Space size={8} wrap>
                {caps?.public_ipv4 ? (
                  <Typography.Text copyable code style={{ fontSize: 13 }}>
                    {caps.public_ipv4}
                  </Typography.Text>
                ) : null}
                {caps?.public_ipv6 ? (
                  <Typography.Text copyable code style={{ fontSize: 13 }}>
                    {caps.public_ipv6}
                  </Typography.Text>
                ) : null}
              </Space>
            ) : null}
            {facts.length > 0 && (
              <Typography.Text type="secondary" style={{ fontSize: 13 }}>
                {facts.join(" · ")}
              </Typography.Text>
            )}
          </Space>
        </Col>
        <Col xs={24} sm={16} lg={10}>
          <Row gutter={16}>
            <Col span={8}>
              <Meter label={t("dashboard.meter.cpu")} pct={cpuPct} color={usageColor} />
            </Col>
            <Col span={8}>
              <Meter label={t("dashboard.meter.memory")} pct={memPct} color={usageColor} />
            </Col>
            <Col span={8}>
              <Meter label={t("dashboard.meter.disk")} pct={diskPct} color={diskColor} />
            </Col>
          </Row>
        </Col>
        <Col xs={24} sm={8} lg={5} style={{ textAlign: "right" }}>
          <Link to="/jabali-admin/server-status">
            <Button type="primary" icon={<ServerOutlined />}>
              {t("dashboard.view_server_status")}
            </Button>
          </Link>
        </Col>
      </Row>
    </Card>
  );
}

// uptimeText reads "up 41 days" past a day, then hours, then minutes.
function uptimeText(seconds: number, t: (k: string, o?: Record<string, unknown>) => string): string {
  const days = Math.floor(seconds / 86400);
  if (days >= 1) return t("dashboard.host.uptime_days", { count: days });
  const hours = Math.floor(seconds / 3600);
  if (hours >= 1) return t("dashboard.host.uptime_hours", { count: hours });
  return t("dashboard.host.uptime_minutes", { count: Math.max(0, Math.floor(seconds / 60)) });
}
