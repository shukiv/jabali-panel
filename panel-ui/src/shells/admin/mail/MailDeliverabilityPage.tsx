// MailDeliverabilityPage — M47 Wave 9 admin score card.
//
// Single GET /api/v1/admin/mail/deliverability; renders the 0-100 score
// with a colour badge and each contributing signal as its own line so
// the operator sees exactly which axis cost what.
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";
import { Alert, Card, Descriptions, Progress, Skeleton, Tag, Typography } from "antd";

import { apiClient } from "../../../apiClient";

type Component = {
  name: string;
  value: number;
  deduction: number;
  detail: string;
};

type ScoreResponse = {
  score: number;
  severity: "ok" | "warning" | "critical";
  generated_at: string;
  components: Component[];
};

const severityColour: Record<ScoreResponse["severity"], string> = {
  ok: "green",
  warning: "gold",
  critical: "red",
};

export const MailDeliverabilityPage = () => {
  const { t } = useTranslation();
  const { data, isLoading, error } = useQuery({
    queryKey: ["admin", "mail", "deliverability"],
    queryFn: async () => (await apiClient.get<ScoreResponse>("/admin/mail/deliverability")).data,
    refetchInterval: 60_000,
  });

  if (isLoading) return <Skeleton active />;
  if (error || !data) {
    return <Alert type="error" message={t("maildeliverabilitypage.failed_to_load_deliverability_score")} />;
  }

  return (
    <Card title={t("maildeliverabilitypage.mail_deliverability_server_wide_score")} variant="outlined">
      <div style={{ marginBottom: 24 }}>
        <Progress
          type="circle"
          percent={data.score}
          strokeColor={severityColour[data.severity]}
          format={(p) => `${p}/100`}
          width={140}
        />
        <Tag color={severityColour[data.severity]} style={{ marginLeft: 16 }}>
          {data.severity.toUpperCase()}
        </Tag>
        <Typography.Text type="secondary" style={{ marginLeft: 16 }}>
          generated {new Date(data.generated_at).toLocaleString()}
        </Typography.Text>
      </div>
      <Descriptions bordered column={1} size="middle">
        {data.components.map((c) => (
          <Descriptions.Item
            key={c.name}
            label={
              <span>
                {c.name}{" "}
                <Tag color={c.deduction === 0 ? "green" : c.deduction >= 20 ? "red" : "gold"}>
                  -{c.deduction}
                </Tag>
              </span>
            }
          >
            <div>
              <strong>{c.value}</strong>
            </div>
            <Typography.Text type="secondary">{c.detail}</Typography.Text>
          </Descriptions.Item>
        ))}
      </Descriptions>
      <Alert
        type="info"
        showIcon
        style={{ marginTop: 16 }}
        message={t("maildeliverabilitypage.how_this_score_works")}
        description={t("maildeliverabilitypage.each_of_the_four_signals_can_subtract_up_to")}
      />
    </Card>
  );
};
