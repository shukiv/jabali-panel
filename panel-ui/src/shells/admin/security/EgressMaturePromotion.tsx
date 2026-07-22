// EgressMaturePromotion — JAB-13. The bulk mature-soak promotion flow for the
// per-user egress firewall, surfaced from the admin GUI to match the CLI's
// `jabali per-user-egress flip-mature`.
//
// The observe -> review drops -> enforce path used to require flipping each
// LEARNING user by hand. Here an operator picks a soak window, previews which
// LEARNING policies are mature enough to graduate (with their soak age + drop
// count), sees whether the operator pin blocks promotion, and — confirm-gated —
// promotes them all at once. Backed by GET /admin/egress/mature-preview (dry
// run) and POST /admin/egress/flip-mature.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  InputNumber,
  Popconfirm,
  Space,
  Table,
  Typography,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { ThunderboltOutlined, WarningOutlined } from "@icons";

import { apiClient } from "../../../apiClient";

type Eligible = {
  user_id: string;
  learning_started_at?: string;
  age_days: number;
  drop_count_24h: number;
};

type FlipMatureResponse = {
  soak_days: number;
  dry_run: boolean;
  pinned: boolean;
  pin_value?: string;
  eligible_count: number;
  eligible: Eligible[];
  flipped?: string[];
  flipped_count: number;
  failed?: Record<string, string>;
};

const columns: ColumnsType<Eligible> = [
  { title: "User", dataIndex: "user_id", key: "user_id" },
  {
    title: "Learning age",
    dataIndex: "age_days",
    key: "age_days",
    render: (d: number) => `${d} day${d === 1 ? "" : "s"}`,
  },
  {
    title: "Drops (24h)",
    dataIndex: "drop_count_24h",
    key: "drop_count_24h",
    render: (n: number) => n.toLocaleString(),
  },
];

export function EgressMaturePromotion() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();
  const [soakDays, setSoakDays] = useState(7);
  const [preview, setPreview] = useState<FlipMatureResponse | null>(null);

  const previewMutation = useMutation({
    mutationFn: async (days: number) => {
      const { data } = await apiClient.get<FlipMatureResponse>(
        `/admin/egress/mature-preview`,
        { params: { soak_days: days } },
      );
      return data;
    },
    onSuccess: (data) => setPreview(data),
    onError: () => message.error("Failed to load mature-soak preview"),
  });

  const promoteMutation = useMutation({
    mutationFn: async (days: number) => {
      const { data } = await apiClient.post<FlipMatureResponse>(
        `/admin/egress/flip-mature`,
        { soak_days: days },
      );
      return data;
    },
    onSuccess: (data) => {
      if (data.pinned) {
        message.warning("Operator pin holds promotion — nothing flipped");
      } else {
        const failed = data.failed ? Object.keys(data.failed).length : 0;
        message.success(
          `Promoted ${data.flipped_count} policy(ies) to ENFORCED` +
            (failed ? `, ${failed} failed` : ""),
        );
      }
      setPreview(data);
      void qc.invalidateQueries({ queryKey: ["user-egress"] });
    },
    onError: () => message.error("Failed to promote mature policies"),
  });

  const pinned = preview?.pinned ?? false;
  const eligibleCount = preview?.eligible_count ?? 0;

  return (
    <Card
      size="small"
      title={
        <Space>
          <ThunderboltOutlined />
          <span>Mature-soak promotion</span>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary">
        Graduate LEARNING policies that have soaked long enough to ENFORCED.
        Preview first — promotion is skipped for any user whose policy is too
        young, and the whole run no-ops when the operator pin
        (<code>/etc/jabali/per-user-egress.mode = learning</code>) is set.
      </Typography.Paragraph>

      <Space wrap style={{ marginBottom: 12 }}>
        <span>Soak window (days):</span>
        <InputNumber
          min={1}
          max={365}
          value={soakDays}
          onChange={(v) => setSoakDays(typeof v === "number" ? v : 7)}
        />
        <Button
          onClick={() => previewMutation.mutate(soakDays)}
          loading={previewMutation.isPending}
        >
          Dry-run preview
        </Button>
        <Popconfirm
          title={`Promote ${eligibleCount} mature policy(ies) to ENFORCED?`}
          description={t("egressmaturepromotion.this_enforces_the_egress_firewall_for_those")}
          okText={t("egressmaturepromotion.promote")}
          okButtonProps={{ danger: true }}
          disabled={pinned || eligibleCount === 0}
          onConfirm={() => promoteMutation.mutate(soakDays)}
        >
          <Button
            type="primary"
            danger
            disabled={pinned || eligibleCount === 0}
            loading={promoteMutation.isPending}
          >
            Promote mature ({eligibleCount})
          </Button>
        </Popconfirm>
      </Space>

      {pinned ? (
        <Alert
          type="warning"
          showIcon
          icon={<WarningOutlined />}
          style={{ marginBottom: 12 }}
          message={t("egressmaturepromotion.operator_pin_active")}
          description={
            <span>
              <code>per-user-egress.mode</code> ={" "}
              <b>{preview?.pin_value || "learning"}</b> — promotion is held. Clear
              the pin file on the host to allow flips.
            </span>
          }
        />
      ) : null}

      {preview && !pinned ? (
        eligibleCount > 0 ? (
          <Table<Eligible>
            rowKey="user_id"
            size="small"
            columns={columns}
            dataSource={preview.eligible}
            pagination={false}
          />
        ) : (
          <Typography.Text type="secondary">
            No LEARNING policies are mature at a {preview.soak_days}-day soak.
          </Typography.Text>
        )
      ) : null}
    </Card>
  );
}
