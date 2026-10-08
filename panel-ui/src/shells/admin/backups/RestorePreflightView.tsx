// RestorePreflightView — GH #1993. The checks of an uploaded backup against
// this server, shown before Restore. A PHP version this server lacks blocks
// the restore; missing PHP extensions, and PostgreSQL, mail, DNS or Docker
// apps turned off here, are warnings: the restore leaves those parts out.
import { Alert, Space, theme } from "antd";
import { CheckCircleOutlined, CloseCircleOutlined, InfoCircleOutlined, WarningOutlined } from "@icons";
import type { RestorePreflight, RestorePreflightCheck } from "../../../apiClient";

function CheckIcon({ level }: { level: RestorePreflightCheck["level"] }) {
  const { token } = theme.useToken();
  const style = { marginTop: 3, flexShrink: 0 };
  switch (level) {
    case "block":
      return <CloseCircleOutlined style={{ ...style, color: token.colorError }} title="Blocks the restore" />;
    case "warn":
      return <WarningOutlined style={{ ...style, color: token.colorWarning }} title="Warning" />;
    case "ok":
      return <CheckCircleOutlined style={{ ...style, color: token.colorSuccess }} title="OK" />;
    default:
      return <InfoCircleOutlined style={{ ...style, color: token.colorTextSecondary }} title="Note" />;
  }
}

export function RestorePreflightView({ preflight, checking }: { preflight?: RestorePreflight | null; checking?: boolean }) {
  if (checking) {
    return <Alert type="info" showIcon message="Checking the backup against this server…" />;
  }
  if (!preflight) return null;
  const warns = preflight.checks.some((c) => c.level === "warn");
  const type = preflight.blocked ? "error" : warns ? "warning" : "success";
  const title = preflight.blocked
    ? "This backup can't be restored here yet"
    : warns
      ? "Some of this backup won't be restored as it is"
      : "The backup fits this server";
  return (
    <Alert
      type={type}
      showIcon
      message={title}
      description={
        <Space direction="vertical" size={4} style={{ width: "100%" }}>
          {preflight.checks.map((c, i) => (
            <div key={`${c.area}-${i}`} style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
              <CheckIcon level={c.level} />
              <span>{c.message}</span>
            </div>
          ))}
        </Space>
      }
    />
  );
}
