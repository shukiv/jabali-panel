// TasksIndicator — topbar icon showing long-running operations in flight
// (panel/system-package updates, backups, malware scans). Polls
// /admin/active-tasks every 3s WHILE tasks are running and every 30s when
// idle; spins + badges the count when tasks are active, and lists them in a
// dropdown. Admin shell only.
import { useTranslation } from "react-i18next";
import { Badge, Button, Dropdown, Empty, Space, Tag, Tooltip, Typography } from "antd";
import type { MenuProps } from "antd";
import { useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";

import { SyncOutlined } from "@icons";

import { apiClient } from "../apiClient";

interface ActiveTask {
  kind: "update" | "backup" | "malware";
  label: string;
  status: "running" | "queued";
  started_at?: string;
}

interface ActiveTasksResponse {
  tasks: ActiveTask[];
  count: number;
}

const deeplinkFor = (kind: ActiveTask["kind"]) =>
  kind === "update"
    ? "/jabali-admin/updates"
    : kind === "backup"
      ? "/jabali-admin/backups"
      : "/jabali-admin/security";

export function TasksIndicator() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const q = useQuery({
    queryKey: ["admin-active-tasks"],
    queryFn: async () => {
      const { data } = await apiClient.get<ActiveTasksResponse>("/admin/active-tasks");
      return data;
    },
    // Adaptive cadence. A flat 3s meant ~28k requests a day per always-open
    // admin tab for a component that renders null ~99% of the time. Poll fast
    // only while something is actually in flight — that is when "shows/clears
    // promptly" matters — and back off to 30s when idle, which still notices a
    // task an operator just started well within a human's attention span.
    // (The previous comment claimed 8s; the code did 3s. Now they agree.)
    refetchInterval: (query) =>
      (query.state.data?.tasks?.length ?? 0) > 0 ? 3000 : 30000,
    retry: false,
  });

  const tasks = q.data?.tasks ?? [];
  const count = tasks.length;

  // Hidden when nothing is running — the query above keeps polling (idle
  // cadence) so the icon reappears the moment a task starts.
  if (count === 0) {
    return null;
  }

  const items: MenuProps["items"] =
    count === 0
      ? [
          {
            key: "empty",
            label: (
              <Empty
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                description={t("tasksindicator.no_tasks_running")}
                style={{ padding: 8, margin: 0 }}
              />
            ),
          },
        ]
      : tasks.map((t, i) => ({
          key: String(i),
          onClick: () => navigate(deeplinkFor(t.kind)),
          label: (
            <Space style={{ minWidth: 220, justifyContent: "space-between" }}>
              <Typography.Text>{t.label}</Typography.Text>
              <Tag color={t.status === "running" ? "processing" : "default"}>{t.status}</Tag>
            </Space>
          ),
        }));

  return (
    <Dropdown menu={{ items }} placement="bottomRight" trigger={["click"]}>
      <Tooltip title={count > 0 ? `${count} task${count > 1 ? "s" : ""} running` : "Background tasks"}>
        <Badge count={count} size="small" offset={[-2, 4]}>
          <Button type="text" icon={<SyncOutlined spin={count > 0} />} aria-label={t("tasksindicator.background_tasks")} />
        </Badge>
      </Tooltip>
    </Dropdown>
  );
}
