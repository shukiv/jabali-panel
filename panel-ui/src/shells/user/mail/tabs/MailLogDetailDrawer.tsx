// MailLogDetailDrawer — GH #261. Per-message delivery trail.
//
// The mail-log file tracer has no subject/body (that needs JMAP), but it does
// carry the full delivery trail keyed by queueId — queued → attempt → connect →
// STARTTLS → delivered / DSN with the remote server's response — which answers
// "why didn't my mail arrive". Fetches GET /mail/logs/detail (scoped server-side
// to the caller's domains).
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";

import { Drawer, Empty, Spin, Timeline, Typography } from "antd";
import { feedback } from "../../../../lib/feedback"; // GH #970: themed toasts

import { apiClient } from "../../../../apiClient";

interface DetailEvent {
  timestamp: string;
  event: string;
  message: string;
  detail?: string;
}
interface DetailResponse {
  queue_id: string;
  from: string;
  to: string;
  events: DetailEvent[];
}

interface Props {
  queueId: string | null;
  open: boolean;
  onClose: () => void;
}

const dotColor = (ev: string): string => {
  if (ev.includes("fail") || ev.includes("error") || ev.includes("bounce"))
    return "red";
  if (ev.includes("delivered") || ev.includes("dsn-success") || ev.includes("completed"))
    return "green";
  return "blue";
};

export const MailLogDetailDrawer = ({ queueId, open, onClose }: Props) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);
  const [data, setData] = useState<DetailResponse | null>(null);

  useEffect(() => {
    if (!open || !queueId) return;
    setLoading(true);
    setData(null);
    apiClient
      .get<DetailResponse>(`/mail/logs/detail?queue_id=${encodeURIComponent(queueId)}`)
      .then((resp) => setData(resp.data))
      .catch((err) =>
        feedback.message.error(err instanceof Error ? err.message : "Could not load trail"),
      )
      .finally(() => setLoading(false));
  }, [open, queueId]);

  return (
    <Drawer title={t("maillogdetaildrawer.delivery_trail")} width={480} open={open} onClose={onClose} destroyOnClose>
      {loading ? (
        <div style={{ textAlign: "center", padding: 48 }}>
          <Spin />
        </div>
      ) : !data || data.events.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("maillogdetaildrawer.no_delivery_trail_found")} />
      ) : (
        <>
          <Typography.Paragraph style={{ marginBottom: 4 }}>
            <Typography.Text type="secondary">From </Typography.Text>
            <Typography.Text code>{data.from || "—"}</Typography.Text>
          </Typography.Paragraph>
          <Typography.Paragraph>
            <Typography.Text type="secondary">To </Typography.Text>
            <Typography.Text code>{data.to || "—"}</Typography.Text>
          </Typography.Paragraph>
          <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
            Subject and body aren't recorded in mail logs — this is the delivery
            trail only.
          </Typography.Paragraph>
          <Timeline
            items={data.events.map((e, i) => ({
              key: i,
              color: dotColor(e.event),
              children: (
                <div>
                  <Typography.Text style={{ fontSize: 12 }} type="secondary">
                    {new Date(e.timestamp).toLocaleString()}
                  </Typography.Text>
                  <div>{e.message || e.event}</div>
                  {e.detail ? (
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {e.detail}
                    </Typography.Text>
                  ) : null}
                </div>
              ),
            }))}
          />
        </>
      )}
    </Drawer>
  );
};
