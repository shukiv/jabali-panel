// EventsTab — admin Notifications > Events. Per-event-kind enable toggle,
// grouped into independently collapsible categories (JAB-381). Defaults seeded
// by panel-api first-boot per models.AllNotificationEventKinds (important = on).
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Collapse, Skeleton, Switch, Table, Tag, Tooltip, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { groupEventsByCategory } from "./eventCategories";

type EventKindRow = {
  kind: string;
  label: string;
  description: string;
  severity: "info" | "warning" | "error" | "critical";
  enabled: boolean;
  default_on: boolean;
};

const LIST_KEY = ["admin", "notification-events"] as const;

// Versioned browser-storage key for the per-category expand/collapse choice.
// Bump the suffix if the stored shape ever changes.
const COLLAPSE_STORAGE_KEY = "jabali.notifEvents.collapse.v1";

const severityColor: Record<EventKindRow["severity"], string> = {
  info: "blue",
  warning: "gold",
  error: "red",
  critical: "magenta",
};

// loadStoredOpen returns the persisted open-category ids, or null when there is
// no (valid) stored choice — which the caller treats as "first visit". An empty
// array is a real choice ("user collapsed everything") and is preserved.
function loadStoredOpen(): string[] | null {
  try {
    const raw = localStorage.getItem(COLLAPSE_STORAGE_KEY);
    if (raw === null) return null; // no key → first visit
    const parsed = JSON.parse(raw) as { v?: number; open?: unknown };
    if (parsed?.v !== 1 || !Array.isArray(parsed.open)) return null;
    if (!parsed.open.every((x) => typeof x === "string")) return null;
    return parsed.open as string[];
  } catch {
    // Malformed / unavailable storage must never break rendering.
    return null;
  }
}

function saveOpen(open: string[]): void {
  try {
    localStorage.setItem(COLLAPSE_STORAGE_KEY, JSON.stringify({ v: 1, open }));
  } catch {
    /* storage unavailable (private mode / quota) — non-fatal */
  }
}

export const EventsTab = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();

  const list = useQuery<{ data: EventKindRow[] }>({
    queryKey: LIST_KEY,
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: EventKindRow[] }>(
        "/admin/settings/notification-events",
      );
      return data;
    },
  });

  // Unchanged mutation semantics (JAB-381 is presentation-only): await the PATCH,
  // then invalidate so the list — and therefore each category's live count —
  // refetches. A failed toggle never mutated the cache, so the switch + count
  // resolve back to server truth on their own; surface the error toast.
  const toggle = async (row: EventKindRow, next: boolean) => {
    try {
      await apiClient.patch(`/admin/settings/notification-events/${row.kind}`, {
        enabled: next,
      });
      qc.invalidateQueries({ queryKey: LIST_KEY });
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Toggle failed");
    }
  };

  // Shared column set — defined once, reused by every category's table.
  const columns = useMemo<ColumnsType<EventKindRow>>(
    () => [
      {
        title: t("eventstab.event"),
        dataIndex: "label",
        width: 280,
        render: (label: string, row) => (
          <div>
            <Typography.Text strong>{label}</Typography.Text>
            <div>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                <code>{row.kind}</code>
              </Typography.Text>
            </div>
          </div>
        ),
      },
      {
        title: t("eventstab.severity"),
        dataIndex: "severity",
        width: 120,
        render: (s: EventKindRow["severity"]) => (
          <Tag color={severityColor[s] ?? "default"}>{s}</Tag>
        ),
      },
      {
        title: t("eventstab.description"),
        dataIndex: "description",
        render: (v: string) => (
          <Typography.Paragraph
            type="secondary"
            style={{
              margin: 0,
              fontSize: 12,
              whiteSpace: "normal",
              wordBreak: "break-word",
            }}
          >
            {v}
          </Typography.Paragraph>
        ),
      },
      {
        title: t("eventstab.enabled"),
        dataIndex: "enabled",
        width: 120,
        render: (enabled: boolean, row) => (
          <Tooltip
            title={
              row.enabled === row.default_on
                ? row.default_on
                  ? "Default: on"
                  : "Default: off"
                : row.default_on
                  ? "Overridden — default is on"
                  : "Overridden — default is off"
            }
          >
            <Switch checked={enabled} onChange={(next) => toggle(row, next)} />
          </Tooltip>
        ),
      },
    ],
    // toggle + t are stable enough for this admin surface; rebuild on language.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [t],
  );

  const groups = useMemo(
    () => groupEventsByCategory(list.data?.data ?? []),
    [list.data],
  );

  // Open-category state. null = "first visit, decide once data loads"; a real
  // array (including []) = the administrator's stored/active choice.
  const [openKeys, setOpenKeys] = useState<string[] | null>(() => loadStoredOpen());

  // First-visit default: expand the first NON-EMPTY category, collapse the rest.
  // Only applies when there is no stored choice; never overwrites the user's.
  useEffect(() => {
    if (openKeys === null && groups.length > 0) {
      setOpenKeys([groups[0].id]);
    }
  }, [openKeys, groups]);

  const handleChange = (keys: string | string[]) => {
    const next = Array.isArray(keys) ? keys : [keys];
    setOpenKeys(next);
    saveOpen(next); // write-through on every change
  };

  if (list.isLoading) {
    return <Skeleton active paragraph={{ rows: 6 }} />;
  }

  // Unknown stored ids (a category currently empty) are simply ignored by
  // Collapse and left in state, so the choice survives the category returning.
  const activeKey = openKeys ?? [];

  const items = groups.map((g) => {
    const total = g.events.length;
    const enabled = g.events.filter((e) => e.enabled).length;
    return {
      key: g.id,
      label: (
        <div
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            gap: 12,
            width: "100%",
          }}
        >
          <Typography.Text strong>{t(g.labelKey)}</Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 12, fontWeight: 400 }}>
            {t("eventstab.category.count", { enabled, total })}
          </Typography.Text>
        </div>
      ),
      children: (
        <Table<EventKindRow>
          rowKey="kind"
          columns={columns}
          dataSource={g.events}
          pagination={false}
          size="small"
          // tableLayout=auto lets the browser size each column to its content;
          // Description takes the leftover width so long sentences flow.
          scroll={{ x: 800 }}
        />
      ),
    };
  });

  return (
    <Collapse
      // ghost + small → compact, table-like sections rather than dashboard cards.
      ghost
      size="small"
      activeKey={activeKey}
      onChange={handleChange}
      items={items}
    />
  );
};
