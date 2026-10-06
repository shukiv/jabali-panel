// RestoreProgressView — GH #1993. A running restore from an uploaded backup:
// which step it is on, what that step is doing, and a bar for the whole
// restore.
import { Progress, Space, Typography } from "antd";
import type { RestoreProgress } from "../../../apiClient";

// overallPercent spreads the steps evenly and fills the current one by its
// own percent.
function overallPercent(p: RestoreProgress): number {
  const steps = Math.max(p.steps, 1);
  const done = Math.min(Math.max(p.step - 1, 0), steps) + Math.min(Math.max(p.percent ?? 0, 0), 100) / 100;
  return Math.round((done / steps) * 100);
}

function stepTitle(p: RestoreProgress): string {
  return p.steps > 1 ? `Step ${p.step} of ${p.steps}: ${p.label}` : p.label;
}

export function RestoreProgressView({ progress, compact }: { progress?: RestoreProgress | null; compact?: boolean }) {
  if (!progress) return null;
  if (compact) {
    return (
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {progress.steps > 1 ? `Step ${progress.step} of ${progress.steps}` : progress.label}
        {progress.detail ? ` — ${progress.detail}` : progress.steps > 1 ? ` — ${progress.label}` : ""}
      </Typography.Text>
    );
  }
  return (
    <Space direction="vertical" size={2} style={{ width: "100%" }}>
      <Typography.Text strong>{stepTitle(progress)}</Typography.Text>
      {progress.detail && <Typography.Text type="secondary">{progress.detail}</Typography.Text>}
      <Progress percent={overallPercent(progress)} status="active" size="small" />
    </Space>
  );
}
