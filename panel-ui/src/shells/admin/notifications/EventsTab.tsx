// EventsTab — admin Notifications > Events. Per-event-kind enable toggles across
// ~60 kinds in 12 categories. Redesigned as a category RAIL + DETAIL (supersedes
// the JAB-381 stacked-accordion): the left rail lists categories with live
// enabled/total counts and a severity hint; the right pane searches within the
// selected category, toggles individual events, and offers bulk Enable-all /
// Disable-all / Reset-to-defaults. A top summary strip plus a global search that
// spans every category round it out. Defaults seeded by panel-api first-boot per
// models.AllNotificationEventKinds. Category mapping lives in eventCategories.ts
// (kept in sync with the Go catalog by a cross-boundary test).
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Button,
  Checkbox,
  Empty,
  Grid,
  Input,
  Skeleton,
  Switch,
  Tag,
  Tooltip,
  Typography,
  theme,
} from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { ReloadOutlined, SearchOutlined } from "@icons";

import { apiClient } from "../../../apiClient";
import { groupEventsByCategory } from "./eventCategories";

type Severity = "info" | "warning" | "error" | "critical";

type EventKindRow = {
  kind: string;
  label: string;
  description: string;
  severity: Severity;
  enabled: boolean;
  default_on: boolean;
};

const LIST_KEY = ["admin", "notification-events"] as const;

// Versioned browser-storage key for the last-selected category. Bump the suffix
// if the stored shape ever changes. A stale/unknown id is ignored (falls back to
// the first category) so the choice survives a category temporarily emptying.
const SEL_STORAGE_KEY = "jabali.notifEvents.selCat.v1";

const severityColor: Record<Severity, string> = {
  info: "blue",
  warning: "gold",
  error: "red",
  critical: "magenta",
};

// Most-severe first — drives the rail's severity-hint dots and their order.
const SEVERITY_ORDER: Severity[] = ["critical", "error", "warning", "info"];

function loadStoredSel(): string | null {
  try {
    return localStorage.getItem(SEL_STORAGE_KEY);
  } catch {
    return null; // storage unavailable (private mode / quota) — non-fatal
  }
}

function saveSel(id: string): void {
  try {
    localStorage.setItem(SEL_STORAGE_KEY, id);
  } catch {
    /* non-fatal */
  }
}

const isOverridden = (e: EventKindRow) => e.enabled !== e.default_on;

const matches = (e: EventKindRow, q: string) => {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  return (
    e.label.toLowerCase().includes(needle) ||
    e.kind.toLowerCase().includes(needle) ||
    e.description.toLowerCase().includes(needle)
  );
};

export const EventsTab = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { token } = theme.useToken();
  const screens = Grid.useBreakpoint();
  const isNarrow = !screens.md;

  const list = useQuery<{ data: EventKindRow[] }>({
    queryKey: LIST_KEY,
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: EventKindRow[] }>(
        "/admin/settings/notification-events",
      );
      return data;
    },
  });

  const rows = useMemo(() => list.data?.data ?? [], [list.data]);
  const groups = useMemo(() => groupEventsByCategory(rows), [rows]);

  // Severity dot palette: token-driven where AntD has a semantic colour so it
  // tracks the theme; "critical" has no token, so a magenta that reads on both
  // light and dark grounds.
  const severityDot: Record<Severity, string> = {
    info: token.colorInfo,
    warning: token.colorWarning,
    error: token.colorError,
    critical: "#eb2f96",
  };

  const [selCat, setSelCat] = useState<string | null>(() => loadStoredSel());
  const [globalQuery, setGlobalQuery] = useState("");
  const [catQuery, setCatQuery] = useState("");
  const [overriddenOnly, setOverriddenOnly] = useState(false);

  // Resolve the effective selection: the stored/active id if it still names a
  // non-empty category, else the first category. Never renders a dead pane.
  const activeCat =
    (selCat && groups.some((g) => g.id === selCat) ? selCat : null) ??
    groups[0]?.id ??
    null;

  // Persist a first-visit / corrected selection back to state + storage once
  // data has loaded, so the rail highlight and the store agree.
  useEffect(() => {
    if (activeCat && activeCat !== selCat) {
      setSelCat(activeCat);
      saveSel(activeCat);
    }
  }, [activeCat, selCat]);

  const selectCat = (id: string) => {
    setSelCat(id);
    saveSel(id);
    setCatQuery("");
    setOverriddenOnly(false);
    setGlobalQuery("");
  };

  // applyMany PATCHes only the kinds that actually change, in parallel, then
  // invalidates once. A failed PATCH never mutated the server, so the list
  // refetch resolves every switch + count back to truth; surface a count.
  const applyMany = async (changes: { kind: string; enabled: boolean }[]) => {
    if (changes.length === 0) return;
    const results = await Promise.allSettled(
      changes.map((c) =>
        apiClient.patch(`/admin/settings/notification-events/${c.kind}`, {
          enabled: c.enabled,
        }),
      ),
    );
    qc.invalidateQueries({ queryKey: LIST_KEY });
    const failed = results.filter((r) => r.status === "rejected").length;
    if (failed > 0) {
      feedback.message.error(
        t("eventstab.bulk_failed", { failed, total: changes.length }),
      );
    }
  };

  const toggleOne = (row: EventKindRow, next: boolean) =>
    applyMany([{ kind: row.kind, enabled: next }]);

  const bulkSet = (events: EventKindRow[], target: boolean) =>
    applyMany(
      events.filter((e) => e.enabled !== target).map((e) => ({ kind: e.kind, enabled: target })),
    );

  const resetToDefaults = (events: EventKindRow[]) =>
    applyMany(
      events
        .filter((e) => e.enabled !== e.default_on)
        .map((e) => ({ kind: e.kind, enabled: e.default_on })),
    );

  // ---- summary strip figures ----
  const totalCount = rows.length;
  const enabledCount = rows.filter((e) => e.enabled).length;
  const overriddenCount = rows.filter(isOverridden).length;

  // ---- detail pane data ----
  const globalMode = globalQuery.trim() !== "";
  const selGroup = groups.find((g) => g.id === activeCat) ?? null;

  const detailEvents: EventKindRow[] = globalMode
    ? rows.filter((e) => matches(e, globalQuery))
    : (selGroup?.events ?? []).filter(
        (e) => matches(e, catQuery) && (!overriddenOnly || isOverridden(e)),
      );

  // ---------- render ----------
  if (list.isLoading) {
    return <Skeleton active paragraph={{ rows: 8 }} />;
  }

  const cardBorder = `1px solid ${token.colorBorderSecondary}`;

  const SeverityDots = ({ events }: { events: EventKindRow[] }) => {
    const present = SEVERITY_ORDER.filter((s) => events.some((e) => e.severity === s));
    return (
      <span style={{ display: "inline-flex", gap: 3 }} aria-hidden>
        {present.map((s) => (
          <span
            key={s}
            style={{
              width: 6,
              height: 6,
              borderRadius: "50%",
              background: severityDot[s],
            }}
          />
        ))}
      </span>
    );
  };

  const rail = (
    <nav
      aria-label={t("eventstab.categories")}
      style={
        isNarrow
          ? {
              display: "flex",
              gap: 8,
              overflowX: "auto",
              padding: "4px 0 12px",
              borderBottom: cardBorder,
              marginBottom: 12,
            }
          : {
              borderRight: cardBorder,
              paddingRight: 12,
              display: "flex",
              flexDirection: "column",
              gap: 2,
            }
      }
    >
      {!isNarrow && (
        <Typography.Text
          type="secondary"
          style={{
            fontSize: 11,
            fontWeight: 600,
            letterSpacing: ".08em",
            textTransform: "uppercase",
            padding: "6px 10px 4px",
          }}
        >
          {t("eventstab.categories")}
        </Typography.Text>
      )}
      {groups.map((g) => {
        const selected = g.id === activeCat && !globalMode;
        const on = g.events.filter((e) => e.enabled).length;
        return (
          <button
            key={g.id}
            type="button"
            aria-current={selected ? "true" : undefined}
            onClick={() => selectCat(g.id)}
            style={{
              display: "flex",
              alignItems: "center",
              gap: 10,
              width: isNarrow ? "auto" : "100%",
              flex: isNarrow ? "0 0 auto" : undefined,
              whiteSpace: "nowrap",
              cursor: "pointer",
              textAlign: "left",
              border: isNarrow ? cardBorder : "1px solid transparent",
              borderRadius: isNarrow ? 999 : 8,
              background: selected ? token.colorPrimaryBg : "transparent",
              color: token.colorText,
              padding: isNarrow ? "7px 12px" : "9px 11px",
              position: "relative",
              boxShadow:
                selected && !isNarrow
                  ? `inset 3px 0 0 0 ${token.colorError}`
                  : undefined,
              borderColor: selected && isNarrow ? token.colorError : undefined,
            }}
          >
            <span style={{ flex: 1, fontSize: 13.5, fontWeight: selected ? 600 : 500 }}>
              {t(g.labelKey)}
            </span>
            {!isNarrow && <SeverityDots events={g.events} />}
            <span
              style={{
                fontSize: 11.5,
                fontVariantNumeric: "tabular-nums",
                color: token.colorTextSecondary,
                background: token.colorBgContainer,
                border: cardBorder,
                borderRadius: 999,
                padding: "1px 8px",
              }}
            >
              {`${on}/${g.events.length}`}
            </span>
          </button>
        );
      })}
    </nav>
  );

  const detailHeader = (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 12,
        flexWrap: "wrap",
        paddingBottom: 12,
        borderBottom: `1px solid ${token.colorSplit}`,
      }}
    >
      <Typography.Title level={5} style={{ margin: 0 }}>
        {globalMode
          ? t("eventstab.search_title", { q: globalQuery.trim() })
          : selGroup
            ? t(selGroup.labelKey)
            : ""}
      </Typography.Title>
      <Typography.Text type="secondary" style={{ fontSize: 13 }}>
        {globalMode
          ? t("eventstab.matches", { count: detailEvents.length })
          : selGroup
            ? t("eventstab.enabled_of_total", {
                enabled: selGroup.events.filter((e) => e.enabled).length,
                total: selGroup.events.length,
              })
            : ""}
      </Typography.Text>
      {!globalMode && selGroup && (
        <div style={{ display: "flex", gap: 8, marginLeft: "auto", flexWrap: "wrap" }}>
          <Button size="small" onClick={() => bulkSet(selGroup.events, true)}>
            {t("eventstab.enable_all")}
          </Button>
          <Button size="small" onClick={() => bulkSet(selGroup.events, false)}>
            {t("eventstab.disable_all")}
          </Button>
          <Button
            size="small"
            type="text"
            icon={<ReloadOutlined />}
            onClick={() => resetToDefaults(selGroup.events)}
          >
            {t("eventstab.reset_defaults")}
          </Button>
        </div>
      )}
    </div>
  );

  const detailTools = !globalMode && (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 12,
        flexWrap: "wrap",
        padding: "12px 0",
        borderBottom: `1px solid ${token.colorSplit}`,
      }}
    >
      <Input
        allowClear
        prefix={<SearchOutlined />}
        placeholder={t("eventstab.filter_category")}
        value={catQuery}
        onChange={(e) => setCatQuery(e.target.value)}
        style={{ flex: 1, minWidth: 180, maxWidth: 360 }}
        aria-label={t("eventstab.filter_category")}
      />
      <Checkbox
        checked={overriddenOnly}
        onChange={(e) => setOverriddenOnly(e.target.checked)}
      >
        {t("eventstab.only_overridden")}
      </Checkbox>
    </div>
  );

  const eventRow = (e: EventKindRow) => (
    <div
      key={e.kind}
      style={{
        display: "flex",
        gap: 14,
        alignItems: "flex-start",
        padding: "14px 4px",
        borderBottom: `1px solid ${token.colorSplit}`,
      }}
    >
      <span
        aria-hidden
        style={{
          marginTop: 6,
          width: 9,
          height: 9,
          borderRadius: "50%",
          background: severityDot[e.severity],
          flex: "none",
        }}
      />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 9, flexWrap: "wrap" }}>
          <Typography.Text strong>{e.label}</Typography.Text>
          <code style={{ fontSize: 11.5, color: token.colorTextTertiary }}>{e.kind}</code>
          {isOverridden(e) && (
            <span
              style={{
                fontSize: 10,
                fontWeight: 600,
                letterSpacing: ".03em",
                textTransform: "uppercase",
                color: token.colorPrimary,
                border: `1px solid ${token.colorPrimaryBorder}`,
                borderRadius: 999,
                padding: "0 7px",
              }}
            >
              {t("eventstab.overridden")}
            </span>
          )}
        </div>
        <Typography.Paragraph
          type="secondary"
          style={{
            margin: "3px 0 0",
            fontSize: 12.5,
            whiteSpace: "normal",
            wordBreak: "break-word",
            maxWidth: "64ch",
          }}
        >
          {e.description}
        </Typography.Paragraph>
      </div>
      <div style={{ display: "flex", alignItems: "center", gap: 14, flex: "none" }}>
        <Tag color={severityColor[e.severity] ?? "default"} style={{ marginInlineEnd: 0 }}>
          {e.severity}
        </Tag>
        <Tooltip
          title={
            e.enabled === e.default_on
              ? e.default_on
                ? t("eventstab.default_on")
                : t("eventstab.default_off")
              : e.default_on
                ? t("eventstab.overridden_default_on")
                : t("eventstab.overridden_default_off")
          }
        >
          <Switch
            checked={e.enabled}
            onChange={(next) => toggleOne(e, next)}
            aria-label={e.label}
          />
        </Tooltip>
      </div>
    </div>
  );

  return (
    <div>
      {/* summary strip */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 10,
          flexWrap: "wrap",
          paddingBottom: 16,
          borderBottom: cardBorder,
          marginBottom: 16,
        }}
      >
        <SummaryStat token={token} value={totalCount} label={t("eventstab.summary_events")} />
        <SummaryStat token={token} value={enabledCount} label={t("eventstab.summary_enabled")} />
        <SummaryStat
          token={token}
          value={overriddenCount}
          label={t("eventstab.summary_overridden")}
          accent={overriddenCount > 0 ? token.colorWarning : undefined}
        />
        <div style={{ flex: 1 }} />
        <Input
          allowClear
          prefix={<SearchOutlined />}
          placeholder={t("eventstab.search_all")}
          value={globalQuery}
          onChange={(e) => setGlobalQuery(e.target.value)}
          style={{ minWidth: 220, maxWidth: 320 }}
          aria-label={t("eventstab.search_all")}
        />
      </div>

      {/* master–detail */}
      <div
        style={
          isNarrow
            ? {}
            : { display: "grid", gridTemplateColumns: "262px 1fr", gap: 20, alignItems: "start" }
        }
      >
        {rail}
        <section style={{ minWidth: 0 }}>
          {detailHeader}
          {detailTools}
          <div>
            {detailEvents.length > 0 ? (
              detailEvents.map(eventRow)
            ) : (
              <Empty
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                description={t("eventstab.no_match")}
                style={{ padding: "48px 0" }}
              />
            )}
          </div>
        </section>
      </div>
    </div>
  );
};

// SummaryStat — one figure + label chip in the top strip.
function SummaryStat({
  token,
  value,
  label,
  accent,
}: {
  token: ReturnType<typeof theme.useToken>["token"];
  value: number;
  label: string;
  accent?: string;
}) {
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
