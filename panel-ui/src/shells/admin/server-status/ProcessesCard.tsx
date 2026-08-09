// ProcessesCard — header counts + a single sortable top-processes
// table merging top_by_cpu + top_by_rss (deduped by pid). CPU and
// RSS columns each carry a sorter; default sort is CPU desc. Per-row
// Kill icon buttons (SIGTERM / SIGKILL) with confirm Modal. The agent
// owns the denylist.
import { useState } from "react";
import { Button, Card, Modal, Space, Statistic, Table } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { CloseOutlined, ThunderboltOutlined } from "@icons";
import { RowActions } from "../../../components/RowActions";
import { UnorderedListOutlined } from "@ant-design/icons";

import { apiClient } from "../../../apiClient";
import type { ProcessesSlice, ProcessTop } from "../../../hooks/useServerStatus";

interface Props {
  processes: ProcessesSlice | null;
}

export function ProcessesCard({ processes }: Props) {
  const [expanded, setExpanded] = useState(false);
  const [pendingKill, setPendingKill] = useState<{ pid: number; comm: string; force: boolean } | null>(null);
  const qc = useQueryClient();
  const p = processes;

  const killMutation = useMutation({
    mutationFn: async ({ pid, force }: { pid: number; force: boolean }) => {
      await apiClient.post(`/admin/processes/${pid}/kill`, { force });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "server-status"] });
      feedback.message.success("Signal sent");
    },
    onError: (e: unknown) => {
      feedback.message.error(e instanceof Error ? e.message : "Kill failed");
    },
  });

  const askKill = (row: ProcessTop, force: boolean) => {
    setPendingKill({ pid: row.pid, comm: row.comm, force });
  };

  const confirmKill = () => {
    if (!pendingKill) return;
    killMutation.mutate({ pid: pendingKill.pid, force: pendingKill.force });
    setPendingKill(null);
  };

  return (
    <>
      <Card
        title={<><UnorderedListOutlined /> Processes</>}
        size="small"
        extra={
          <Button size="small" onClick={() => setExpanded((v) => !v)}>
            {expanded ? "Hide top-10" : "Show top-10"}
          </Button>
        }
      >
        <Space size={24} wrap>
          <Statistic title="Total" value={p?.total ?? 0} />
          <Statistic title="Running" value={p?.running ?? 0} />
          <Statistic title="Sleeping" value={p?.sleeping ?? 0} />
          <Statistic
            title="Zombie"
            value={p?.zombie ?? 0}
            valueStyle={{ color: (p?.zombie ?? 0) > 0 ? "#cf1322" : undefined }}
          />
        </Space>
        {expanded && p ? (
          <ProcTable rows={mergeTops(p)} onKill={askKill} />
        ) : null}
      </Card>
      <Modal
        open={!!pendingKill}
        title={pendingKill ? `${pendingKill.force ? "SIGKILL" : "SIGTERM"} pid ${pendingKill.pid} (${pendingKill.comm})?` : ""}
        okText={pendingKill?.force ? "SIGKILL" : "SIGTERM"}
        okButtonProps={{ danger: true }}
        onOk={confirmKill}
        onCancel={() => setPendingKill(null)}
      >
        {pendingKill?.force ? (
          <p>SIGKILL is unmaskable — the process gets no chance to flush state. Use SIGTERM first when in doubt.</p>
        ) : (
          <p>SIGTERM asks the process to clean up and exit. If it ignores the signal, retry with Force.</p>
        )}
      </Modal>
    </>
  );
}

interface ProcTableProps {
  rows: ProcessTop[];
  onKill: (r: ProcessTop, force: boolean) => void;
}

function ProcTable({ rows, onKill }: ProcTableProps) {
  return (
    <Table<ProcessTop>
      rowKey="pid"
      size="small"
      style={{ marginTop: 12 }}
      dataSource={rows}
      pagination={false}
      scroll={{ x: "max-content" }}
      columns={[
        { title: "PID", dataIndex: "pid", width: 80, sorter: (a, b) => a.pid - b.pid },
        { title: "Comm", dataIndex: "comm", ellipsis: true, sorter: (a, b) => a.comm.localeCompare(b.comm) },
        { title: "User", dataIndex: "user", width: 110, sorter: (a, b) => a.user.localeCompare(b.user) },
        {
          title: "CPU",
          dataIndex: "cpu_percent",
          width: 90,
          sorter: (a, b) => (a.cpu_percent ?? 0) - (b.cpu_percent ?? 0),
          defaultSortOrder: "descend" as const,
          render: (_: unknown, r: ProcessTop) => `${(r.cpu_percent ?? 0).toFixed(1)}%`,
        },
        {
          title: "RAM",
          dataIndex: "rss_kb",
          width: 90,
          sorter: (a, b) => (a.rss_kb ?? 0) - (b.rss_kb ?? 0),
          render: (_: unknown, r: ProcessTop) => humanKB(r.rss_kb),
        },
        {
          title: "",
          width: 70,
          align: "right" as const,
          render: (_: unknown, r: ProcessTop) => (
            <RowActions
              actions={[
                { key: "term", label: "Term", icon: <CloseOutlined />, onClick: () => onKill(r, false), tooltip: "SIGTERM (graceful)" },
                { key: "kill", label: "Kill", icon: <ThunderboltOutlined />, danger: true, onClick: () => onKill(r, true), tooltip: "SIGKILL (force)" },
              ]}
            />
          ),
        },
      ]}
    />
  );
}

// mergeTops returns the union of top_by_cpu + top_by_rss deduped by
// pid. A high-RAM but low-CPU process and a high-CPU but low-RAM
// process should both surface in the same sortable table; merging
// the two server-side lists in the client is the cheapest way to
// get there without bloating the polling payload.
function mergeTops(p: ProcessesSlice): ProcessTop[] {
  const seen = new Map<number, ProcessTop>();
  for (const r of p.top_by_cpu ?? []) seen.set(r.pid, r);
  for (const r of p.top_by_rss ?? []) {
    if (!seen.has(r.pid)) seen.set(r.pid, r);
  }
  return Array.from(seen.values());
}

function humanKB(kb: number): string {
  if (!kb) return "0";
  const units = ["KB", "MB", "GB"];
  let v = kb;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`;
}
