// BulkWhmDrawer — ADR-0095 decision 3. Creates N draft migration_jobs
// in one shot, all sharing a batch_id. Used for the WHM "migrate every
// account on this server" workflow; pairs with the existing
// CreateMigrationDrawer which still handles single-account flows.
//
// v1 surface: paste accounts as newline / comma-separated list. The
// M35.2 follow-up replaces the textarea with a discover-accounts call
// that returns the cPanel account list from a live WHM API session.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Alert,
  Button,
  Drawer,
  Form,
  Input,
  Space,
  Typography,
  message,
} from "antd";
import { useMutation } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";

type BulkResp = {
  batch_id: string;
  jobs: Array<{ id: string; source_user: string }>;
};

interface Props {
  open: boolean;
  onClose: () => void;
  onCreated?: (batchID: string) => void;
}

export const BulkWhmDrawer = ({ open, onClose, onCreated }: Props) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<{ source_host: string; accounts: string }>();
  const [result, setResult] = useState<BulkResp | null>(null);

  const mut = useMutation({
    mutationFn: async (vals: { source_host: string; accounts: string }) => {
      // Accept newline OR comma OR space separated lists; trim each
      // entry, drop empties. Server-side bulk endpoint silently skips
      // duplicate (host, user, kind) tuples so re-submission after a
      // partial run is idempotent.
      const accounts = vals.accounts
        .split(/[\s,]+/)
        .map((s) => s.trim())
        .filter(Boolean);
      const { data } = await apiClient.post<BulkResp>("/admin/migrations/bulk", {
        source_kind: "whm_pkgacct",
        source_host: vals.source_host,
        accounts,
      });
      return data;
    },
    onSuccess: (data) => {
      setResult(data);
      message.success(`Batch ${data.batch_id} — ${data.jobs.length} jobs queued`);
      onCreated?.(data.batch_id);
    },
    onError: (e: unknown) => {
      const detail = (e as { response?: { data?: { detail?: string } } })?.response
        ?.data?.detail;
      message.error(detail ?? "Bulk create failed");
    },
  });

  const handleDone = () => {
    setResult(null);
    form.resetFields();
    onClose();
  };

  return (
    <Drawer
      open={open}
      onClose={handleDone}
      width={520}
      title={t("bulkwhmdrawer.create_whm_migration_batch")}
      destroyOnClose
    >
      {!result ? (
        <Form
          form={form}
          layout="vertical"
          onFinish={(vals) => mut.mutate(vals)}
        >
          <Alert
            type="info"
            showIcon
            message={t("bulkwhmdrawer.bulk_whm")}
            description={t("bulkwhmdrawer.creates_one_migration_job_per_account_all_sh")}
          />
          <Form.Item
            label={t("bulkwhmdrawer.source_host")}
            name="source_host"
            rules={[{ required: true, message: "Source WHM host required" }]}
            tooltip={t("bulkwhmdrawer.hostname_or_ip_of_the_source_whm_server_priv")}
          >
            <Input placeholder="src.example.com" />
          </Form.Item>
          <Form.Item
            label={t("bulkwhmdrawer.accounts")}
            name="accounts"
            rules={[{ required: true, message: "At least one account required" }]}
            tooltip={t("bulkwhmdrawer.one_account_per_line_or_comma_separated_exis")}
          >
            <Input.TextArea
              rows={8}
              placeholder="alice&#10;bob&#10;charlie"
              autoSize={{ minRows: 6, maxRows: 16 }}
            />
          </Form.Item>
          <Space>
            <Button type="primary" htmlType="submit" loading={mut.isPending}>
              Create batch
            </Button>
            <Button onClick={handleDone}>Cancel</Button>
          </Space>
        </Form>
      ) : (
        <Space direction="vertical" size="large" style={{ width: "100%" }}>
          <Alert
            type="success"
            showIcon
            message={`Batch ${result.batch_id} created`}
            description={`${result.jobs.length} migration_jobs queued in draft state. Configure secrets + pull + import per job from the migrations list.`}
          />
          <Typography.Text type="secondary">
            Cancel the entire batch with DELETE /admin/migrations/batches/{result.batch_id}.
          </Typography.Text>
          <Button type="primary" onClick={handleDone}>
            Done
          </Button>
        </Space>
      )}
    </Drawer>
  );
};
