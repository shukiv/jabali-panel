// RunStatusSummary collapses a backup run's six status counters into a single
// Tag stack so the row at a glance answers "is this run still working / did
// any fail?" — the full breakdown lives in the Backups page's expanded child
// table. Shared by the Backups page and the admin Dashboard (GH #1249).
import { Space, Tag } from "antd";

export interface RunStatusCounts {
  total: number;
  succeeded: number;
  failed: number;
  running: number;
  queued: number;
  cancelled: number;
  partial: number;
}

export const RunStatusSummary = ({ run }: { run: RunStatusCounts }) => {
  const tags: { color: string; text: string }[] = [];
  if (run.running > 0) tags.push({ color: "blue", text: `${run.running} running` });
  if (run.queued > 0) tags.push({ color: "default", text: `${run.queued} queued` });
  if (run.failed > 0) tags.push({ color: "red", text: `${run.failed} failed` });
  if (run.partial > 0) tags.push({ color: "gold", text: `${run.partial} partial` });
  if (run.cancelled > 0) tags.push({ color: "default", text: `${run.cancelled} cancelled` });
  if (run.succeeded > 0) tags.push({ color: "green", text: `${run.succeeded} succeeded` });
  if (tags.length === 0) tags.push({ color: "default", text: `${run.total} jobs` });
  return (
    <Space size={4} wrap>
      {tags.map((t) => (
        <Tag key={t.text} color={t.color}>
          {t.text}
        </Tag>
      ))}
    </Space>
  );
};
