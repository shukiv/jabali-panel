// DRStatusCard — admin Server Status card for the DR standby's freshness
// (GH #1169, #331 blueprint step 5). Surfaces role / peer / last-sync age +
// a freshness badge that agrees with the dr.sync.stalled notification, so a
// stalling replica is visible from the panel instead of only via
// `jabali dr status` on the standby.
import { useEffect, useState } from "react";
import { Card, Descriptions, Tag, Tooltip, Typography } from "antd";

import { apiClient } from "../../../apiClient";

interface DRStatus {
  role: string;
  is_standby: boolean;
  paired: boolean;
  peer: string;
  destination_id: string;
  destination_name: string;
  paired_at?: string;
  last_sync_at?: string;
  last_sync_status?: string;
  last_snapshot_id?: string;
  last_sync_error?: string;
  sync_age_seconds: number;
  stale_threshold_seconds: number;
  stalled: boolean;
}

function humanizeAge(seconds: number): string {
  if (seconds < 0) return "—";
  if (seconds < 60) return `${seconds}s ago`;
  const m = Math.floor(seconds / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m ago`;
  return `${Math.floor(h / 24)}d ${h % 24}h ago`;
}

function FreshnessBadge({ s }: { s: DRStatus }) {
  if (!s.is_standby) return <Tag>Primary</Tag>;
  if (s.stalled) {
    return (
      <Tooltip title={`No fresh snapshot in over ${Math.round(s.stale_threshold_seconds / 60)} min — fix before you need to promote.`}>
        <Tag color="red">Stalled</Tag>
      </Tooltip>
    );
  }
  if (s.last_sync_status === "error") return <Tag color="orange">Sync error</Tag>;
  return <Tag color="green">In sync</Tag>;
}

export function DRStatusCard() {
  const [status, setStatus] = useState<DRStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const load = () =>
      apiClient
        .get<DRStatus>("/admin/dr/status")
        .then((r) => {
          if (alive) {
            setStatus(r.data);
            setErr(null);
          }
        })
        .catch((e: unknown) => {
          if (alive) setErr(e instanceof Error ? e.message : "Failed to load DR status");
        });
    void load();
    // Refresh on the same cadence the standby syncs (60s) so the age stays live.
    const t = setInterval(() => void load(), 60_000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  const title = (
    <span>
      Disaster Recovery {status ? <FreshnessBadge s={status} /> : null}
    </span>
  );

  if (err) {
    return (
      <Card title="Disaster Recovery" size="small">
        <Typography.Text type="secondary">{err}</Typography.Text>
      </Card>
    );
  }
  if (!status) {
    return <Card title="Disaster Recovery" size="small" loading />;
  }

  // A plain primary with no DR pairing: one informational line, no noise.
  if (!status.is_standby && !status.paired) {
    return (
      <Card title={title} size="small">
        <Typography.Text type="secondary">
          This server is a primary. No DR standby role is configured here.
        </Typography.Text>
      </Card>
    );
  }

  return (
    <Card title={title} size="small">
      <Descriptions column={1} size="small" colon>
        <Descriptions.Item label="Role">
          <Tag color={status.is_standby ? "geekblue" : "default"}>{status.role}</Tag>
        </Descriptions.Item>
        <Descriptions.Item label="Peer">{status.peer || "—"}</Descriptions.Item>
        <Descriptions.Item label="DR destination">{status.destination_name || status.destination_id || "—"}</Descriptions.Item>
        {status.is_standby && (
          <Descriptions.Item label="Last sync">
            {status.last_sync_at ? (
              <Tooltip title={status.last_sync_at}>{humanizeAge(status.sync_age_seconds)}</Tooltip>
            ) : (
              "never (since pairing)"
            )}
          </Descriptions.Item>
        )}
        {status.is_standby && status.last_snapshot_id && (
          <Descriptions.Item label="Applied snapshot">
            <code>{status.last_snapshot_id}</code>
          </Descriptions.Item>
        )}
        {status.is_standby && status.last_sync_error && (
          <Descriptions.Item label="Last error">
            <Typography.Text type="danger">{status.last_sync_error}</Typography.Text>
          </Descriptions.Item>
        )}
      </Descriptions>
    </Card>
  );
}
