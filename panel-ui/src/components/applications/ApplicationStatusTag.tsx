// ApplicationStatusTag — the shared install-status badge for both application
// lists. Renders the status meta (color/icon/label); a failed row with a
// last_error wraps the tag in a Tooltip exposing the error. One failure
// presentation for both lists (JAB-334 AC2).
import { Tag, Tooltip } from "antd";
import { applicationStatusMeta, type ApplicationStatus } from "../../utils/applicationStatus";

export function ApplicationStatusTag({
  status,
  lastError,
}: {
  status: ApplicationStatus;
  lastError?: string;
}) {
  const meta = applicationStatusMeta(status);
  const tag = (
    <Tag color={meta.color} icon={meta.icon}>
      {meta.label}
    </Tag>
  );
  if (status === "failed" && lastError) {
    return <Tooltip title={lastError}>{tag}</Tooltip>;
  }
  return tag;
}
