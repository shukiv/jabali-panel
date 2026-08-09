// MyProfileScheduleCard — tenant scheduled-backup control (GH #454 Step 4).
// The tenant owns WHAT (content) + WHERE (destination) + on/off + retention;
// the ADMIN owns the TIME (server_settings.tenant_backup_cron), shown read-only.
// Gated on the hosting package's scheduled_backups_enabled. Unlike the on-demand
// card, a schedule needs a real destination row — the implicit "local default"
// is skipped by the scheduler fan-out — so only concrete destinations are offered.
import { useTranslation } from "react-i18next";
import { Alert, Button, Card, InputNumber, Select, Space, Switch, Typography } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { ClockCircleOutlined } from "@icons";
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import { extractApiError } from "../../apiErrors";
import { shortDateTimeUTC } from "../../utils/datetime";

type ScheduleView = {
  exists: boolean;
  enabled: boolean;
  content: string;
  destination_id?: string;
  keep_daily?: number | null;
  cron_expr: string;
  next_firings?: string[];
  scheduled_backups_enabled: boolean;
  max_backups: number;
};

export const MyProfileScheduleCard = () => {
  const { t } = useTranslation();
  const schedQuery = useQuery({
    queryKey: ["me-backup-schedule"],
    queryFn: async () => (await apiClient.get<{ data: ScheduleView }>("/me/backup-schedule")).data.data,
  });
  // Shares the on-demand card's query key so the two cards fetch destinations once.
  const destQuery = useQuery({
    queryKey: ["me-backup-destinations"],
    queryFn: async () =>
      (
        await apiClient.get<{ data: { id: string; name: string; kind: string }[]; allow_local: boolean }>(
          "/me/backups/destinations",
        )
      ).data,
  });

  const [enabled, setEnabled] = useState(false);
  const [content, setContent] = useState("full");
  const [destinationId, setDestinationId] = useState("");
  const [keepDaily, setKeepDaily] = useState<number | null>(null);
  const [saving, setSaving] = useState(false);

  const view = schedQuery.data;
  useEffect(() => {
    if (!view) return;
    setEnabled(view.enabled);
    setContent(view.content || "full");
    setDestinationId(view.destination_id ?? "");
    setKeepDaily(view.keep_daily ?? null);
  }, [view]);

  const dests = destQuery.data?.data ?? [];
  const destOptions = dests.map((d) => ({ value: d.id, label: `${d.name} (${d.kind})` }));

  const handleSave = async () => {
    setSaving(true);
    try {
      await apiClient.put("/me/backup-schedule", {
        enabled,
        content,
        destination_id: destinationId,
        keep_daily: keepDaily,
      });
      feedback.message.success("Schedule saved");
      schedQuery.refetch();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Save failed"));
    } finally {
      setSaving(false);
    }
  };

  const nextRun = view?.next_firings?.[0];

  return (
    <Card
      title={
        <span>
          <ClockCircleOutlined style={{ marginRight: 8 }} />
          Scheduled backups
        </span>
      }
      style={{ marginTop: 16 }}
      loading={schedQuery.isLoading}
    >
      {view && !view.scheduled_backups_enabled ? (
        <Alert type="info" showIcon message={t("myprofileschedulecard.scheduled_backups_are_not_included_in_your_h")} />
      ) : (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            {nextRun ? (
              <>
                Your host runs your scheduled backup next at <strong>{shortDateTimeUTC(nextRun)}</strong>.
              </>
            ) : (
              <>Your host sets when scheduled backups run.</>
            )}{" "}
            You choose what to back up and where — the timing is set by your host.
          </Typography.Paragraph>
          <Space wrap align="center">
            <Switch checked={enabled} onChange={setEnabled} checkedChildren="On" unCheckedChildren="Off" />
            <Select
              value={content}
              onChange={setContent}
              style={{ minWidth: 150 }}
              options={[
                { value: "full", label: "Full account" },
                { value: "files", label: "Files only" },
                { value: "database", label: "Databases only" },
              ]}
            />
            <Select
              value={destinationId}
              onChange={setDestinationId}
              style={{ minWidth: 200 }}
              placeholder={t("myprofileschedulecard.destination")}
              options={destOptions}
              notFoundContent="No allowed destinations"
            />
            <Space>
              <span>Keep</span>
              <InputNumber
                min={1}
                max={view?.max_backups || 1}
                value={keepDaily ?? undefined}
                onChange={(v) => setKeepDaily(typeof v === "number" ? v : null)}
                style={{ width: 80 }}
              />
              <span>copies</span>
            </Space>
            <Button type="primary" loading={saving} onClick={handleSave}>
              Save schedule
            </Button>
          </Space>
        </Space>
      )}
    </Card>
  );
};
