// SummaryStat — one figure + label chip for the summary strip at the top of a
// rail + detail page (Notifications > Events, API docs).
import { theme } from "antd";

export function SummaryStat({
  value,
  label,
  accent,
}: {
  value: number;
  label: string;
  accent?: string;
}) {
  const { token } = theme.useToken();
  return (
    <div
      style={{
        display: "flex",
        alignItems: "baseline",
        gap: 7,
        padding: "7px 13px",
        border: `1px solid ${token.colorBorderSecondary}`,
        borderRadius: 10,
        background: token.colorFillQuaternary,
      }}
    >
      <b
        style={{
          fontSize: 16,
          fontVariantNumeric: "tabular-nums",
          color: accent ?? token.colorText,
        }}
      >
        {value}
      </b>
      <span style={{ color: token.colorTextSecondary, fontSize: 12.5 }}>{label}</span>
    </div>
  );
}
