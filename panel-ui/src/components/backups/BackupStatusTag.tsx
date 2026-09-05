// BackupStatusTag — shared status pill for the Account/System backup
// tables. Adds an icon per state so the at-a-glance scan picks out
// running (spinner) and failed (red X) without reading the label.
import { Tag } from "antd";
import type { ReactNode } from "react";

import {
  CheckCircleOutlined,
  ClockCircleOutlined,
  CloseCircleOutlined,
  ExclamationCircleOutlined,
  LoadingOutlined,
  PauseCircleOutlined,
} from "@icons";

interface BackupStatusTagProps {
  status: string;
}

interface StatusVisual {
  color: string;
  icon: ReactNode;
}

const visuals: Record<string, StatusVisual> = {
  succeeded: { color: "green", icon: <CheckCircleOutlined /> },
  running: { color: "blue", icon: <LoadingOutlined /> },
  queued: { color: "default", icon: <ClockCircleOutlined /> },
  partial: { color: "gold", icon: <ExclamationCircleOutlined /> },
  failed: { color: "red", icon: <CloseCircleOutlined /> },
  cancelled: { color: "default", icon: <PauseCircleOutlined /> },
};

export const BackupStatusTag = ({ status }: BackupStatusTagProps) => {
  const v = visuals[status] ?? { color: "default", icon: null };
  return (
    <Tag color={v.color} icon={v.icon}>
      {status}
    </Tag>
  );
};
